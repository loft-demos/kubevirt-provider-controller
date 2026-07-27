# kubevirt-provider-controller Helm chart

The chart installs the `KubeVirtProviderCluster` CRD and the controller with
the cluster-wide permissions required to manage vCluster Platform resources.

## Install from GHCR

```bash
helm upgrade --install kubevirt-provider-controller \
  oci://ghcr.io/loft-demos/charts/kubevirt-provider-controller \
  --version 0.2.0 \
  --namespace kubevirt-provider-controller-system \
  --create-namespace
```

The chart version selects the controller image tag by default. Override the
image repository when installing a chart published from a fork:

```bash
--set image.repository=ghcr.io/OWNER/kubevirt-provider-controller
```

## Important values

| Value | Default | Description |
|---|---:|---|
| `image.repository` | `ghcr.io/loft-demos/kubevirt-provider-controller` | Controller image repository |
| `image.tag` | chart `appVersion` | Optional independent image tag |
| `image.digest` | empty | Optional immutable digest; takes precedence over tag |
| `controller.workers` | `2` | Reconciliation workers |
| `controller.resyncPeriod` | `30s` | Full informer resync period |
| `controller.logLevel` | `2` | klog verbosity |
| `serviceAccount.create` | `true` | Create the controller ServiceAccount |
| `rbac.create` | `true` | Create ClusterRole and ClusterRoleBinding |

`replicaCount` must remain `1` until leader election is implemented.

Helm installs files under `crds/` on first installation but does not upgrade or
delete CRDs. Review and apply CRD changes explicitly before upgrading across an
API schema change.

Version 0.2.0 requires `spec.virtualCluster.owner.user` or
`spec.virtualCluster.owner.team`. Values are Platform resource names, not login
email addresses. The controller uses a TTL-limited token kubeconfig through the
Platform proxy, so provider VCIs do not require a direct API endpoint.
The generated ClusterRole permits user/group impersonation for the temporary
kubeconfig request. Only trusted infrastructure administrators should be able
to create or modify `KubeVirtProviderCluster` resources.
