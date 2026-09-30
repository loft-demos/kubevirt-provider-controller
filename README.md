# KubeVirt Provider Controller

`kubevirt-provider-controller` turns a dedicated Private Node vCluster into a
connected vCluster Platform control-plane cluster and creates a KubeVirt
`NodeProvider` that targets it.

The project name deliberately omits `cluster`: the controller's primary API is
already named `KubeVirtProviderCluster`, while the shorter repository and image
name remain readable in commands, logs, and labels.

## Lifecycle

For each `KubeVirtProviderCluster`, the controller:

1. Creates a `VirtualClusterInstance` from a supplied template. The template
   must enable Private Nodes and the v2 Argo CD connector. The configured
   Platform User or Team resource is copied to `spec.owner`.
2. Waits for `VirtualClusterReady` and `VirtualClusterOnline`.
3. Creates a cluster-scoped Platform `Cluster` with `networkPeer: true` and
   `unusable: true`.
4. Gets that Cluster's scoped agent enrollment key.
5. Requests a short-lived token kubeconfig and uses the vCluster Platform proxy
   only to create `vcluster-platform/loft-agent-bootstrap` in the vCluster.
   The temporary access key expires after the configured TTL. The kubeconfig is
   held in memory and discarded; it is never stored in the management cluster.
   The kubeconfig request impersonates the configured owner so Platform can own
   and scope the temporary access key correctly.
6. Creates a v2 `ArgoCDApplication` targeting the VCI's `vCluster` destination.
   Argo installs the Platform chart in `agentOnly` mode, using the chart's
   `tokenSecretRef` support. The enrollment token is not placed in the Argo
   application or Helm values.
7. Waits for the connected Cluster to reach `Initialized`.
8. Creates the KubeVirt `NodeProvider`, forcing its `clusterRef` to the newly
   connected cluster.
9. Waits for the NodeProvider to reach `Available`.

Deletion is ordered in reverse: NodeProvider, Argo application, connected
Cluster, and finally the VCI. Existing objects without this controller's exact
ownership labels are never adopted or deleted.

The controller watches the four resources it creates (selected by its
`app.kubernetes.io/managed-by` label), so a status change on any of them
reconciles the owning `KubeVirtProviderCluster` immediately. The informer
resync (`--resync-period`, default `5m`) is only a safety net.

## Dual Platform identity

The VCI intentionally has two Platform identities:

- `VirtualClusterInstance`: tenant-cluster lifecycle, v2 Argo registration,
  user access, and the Platform proxy.
- `Cluster`: connected control-plane lifecycle and the object referenced by
  `NodeProvider.spec.kubeVirt.clusterRef`.

These identities use different API resources and credentials. The Argo
registration key is target-scoped and is not reused as the connected-cluster
agent key. There is no API identity collision.

The connected Cluster remains `unusable: true`. This is intentional: Platform
must not schedule Spaces or other tenant clusters onto an infrastructure VCI
whose only purpose is to host KubeVirt. Do not expose this template as a normal
tenant offering.

## Requirements

- vCluster Platform with the v2 Argo CD integration configured.
- A Platform chart version containing `tokenSecretRef` support.
- A `VirtualClusterTemplate` that enables both `privateNodes.enabled` and
  `integrations.argoCD`.
- A Platform User or Team resource name for `spec.virtualCluster.owner`. Use
  the resource name (for example `admin`), not an email address.
- At least one Private Node joined to the provider VCI.
- For hardware virtualization, `/dev/kvm` exposed on that Private Node.
- Egress from the VCI to the configured Platform `loftHost`.

The controller runs against the vCluster Platform management Kubernetes API.
Its service account therefore needs access to Platform management resources and
the `clusters/accesskey` and `virtualclusterinstances/kubeconfig` subresources.
It also needs Kubernetes `impersonate` permission for users and groups to make
the kubeconfig request as the configured VCI owner. Access to create or update
`KubeVirtProviderCluster` resources must therefore be restricted to trusted
Platform infrastructure administrators.
The controller Pod must also be able to reach the Platform URL emitted in the
temporary kubeconfig.

`ArgoCDIntegrationSynced` is intentionally not a hard enrollment gate. That VCI
condition aggregates all Argo applications, and an unrelated application
failure must not prevent provider enrollment. The controller instead observes
its own agent `ArgoCDApplication`; successful synchronization proves the v2
connector is usable for this workflow.

## Template parameters

A `VirtualClusterTemplate` may expose `parameters` whose values the referenced
template renders into its helm values (for example the autoNodes node pool
`quantity`). Pass those values per provider cluster with
`spec.virtualCluster.parameterValues`, a structured object keyed by each
template parameter's `variable`:

```yaml
spec:
  virtualCluster:
    templateRef:
      name: private-node-kubevirt-provider
    parameterValues:
      nodePoolQuantity: 3
```

The controller marshals `parameterValues` to YAML and sets it as the generated
`VirtualClusterInstance` `spec.parameters`, so any parameters the template
defines are supported without editing the controller. For raw pass-through the
legacy `spec.virtualCluster.parameters` string is still accepted, but the two
fields are mutually exclusive; prefer `parameterValues`.

## Tenants and capacity types

These fields target the tenancy and KubeVirt capacity model in vCluster
Platform 4.13 (alpha builds at the time of writing).

- `spec.tenant.name` scopes the generated NodeProvider to one Platform Tenant.
  `spec.tenant.assignment: Exclusive` (default) stamps
  `tenant.vcluster.com/exclusive-to`: the platform owns the provider and lends it
  to that tenant alone. `Owned` stamps `tenant.vcluster.com/owner`: the tenant
  owns it. Without `spec.tenant` the provider is platform-owned and each
  tenant's baseline rules decide who may use it. Labels other writers set on
  the NodeProvider, such as Platform's projected scope keys, are preserved.
- `spec.nodeProvider.capacityTypes` stamps
  `kubevirt.vcluster.com/capacity-type` on every template NodeType that does not
  set it. `on-demand` places VMs on shared hosts (the tenant cluster's own
  nodes); `reserved` places them only on bare metal a tenant provisioned for VMs
  into this cluster with the NodeClaim property
  `join-kubevirt-infra.vcluster.com/provider-ref`. Platform prefers on-demand
  when both are allowed.

Provisioning bare metal for VMs into a Private Node tenant cluster is expected
to work (the join reads `kube-system/kubeadm-config`, which vCluster private
nodes create) but is not yet verified. Keep at least one `autoNodes` node in the
provider tenant cluster regardless: the Platform agent must run before
Platform can join any machine to it.

## vCluster device operator

The sample NodeProvider enables `deploy.vClusterDeviceOperator` and pins
`0.2.0`; Platform's built-in default (`0.0.7`) predates the `Bridge` CRD. The
operator patches the KubeVirt CR's `permittedHostDevices` and feature gates from
the devices a `HostDevice` discovers, so do not also set those in the KubeVirt
Helm values: Helm and the operator would overwrite each other.

In a lab without GPUs the useful part is the `Bridge` CRD: see
[config/samples/lab-vm-network.yaml](config/samples/lab-vm-network.yaml) to put
VMs directly on a lab VLAN through a bridge NetworkAttachmentDefinition. Multus
must be installed in the provider tenant cluster; Platform deploys Multus only
for Metal3 providers.

## Install

Published releases can be installed from the OCI chart in GHCR:

```bash
helm upgrade --install kubevirt-provider-controller \
  oci://ghcr.io/loft-demos/charts/kubevirt-provider-controller \
  --version VERSION \
  --namespace kubevirt-provider-controller-system \
  --create-namespace
```

For a local checkout, install the same chart directly:

```bash
helm upgrade --install kubevirt-provider-controller ./chart \
  --namespace kubevirt-provider-controller-system \
  --create-namespace
```

The original Kustomize deployment remains available for development. Build and
publish the image, change its reference in `config/manager/deployment.yaml`, and
run:

```bash
kubectl apply -k config
```

Create or adapt the example template and composite resource:

```bash
kubectl apply -f config/samples/private-node-provider-template.yaml
kubectl apply -f config/samples/local-kvm.yaml
kubectl -n p-platform get kubevirtprovidercluster local-kvm-provider -w
```

See [local KVM validation](docs/local-kvm-validation.md) before treating an
`Available` NodeProvider as proof that nested virtualization works.

## Development

```bash
make test
make build
make chart-lint
```

Release publication is split into two workflows:

- `publish-image.yaml` publishes `linux/amd64` and `linux/arm64` images to
  `ghcr.io/<repository-owner>/kubevirt-provider-controller`. Main publishes
  `edge` and SHA tags; GitHub releases publish semantic-version and stable
  `latest` tags.
- `publish-chart.yaml` packages the release version and pushes the chart to
  `oci://ghcr.io/<repository-owner>/charts/kubevirt-provider-controller`.

The API is deliberately `v1alpha1`. This is a PoV controller built against the
Platform v1 management APIs visible in this workspace, not a released vCluster
Platform feature or compatibility commitment.
