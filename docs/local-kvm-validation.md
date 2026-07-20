# Local KVM passthrough validation

The local path proves KVM-backed VM lifecycle without claiming GPU passthrough.
The sample sets KubeVirt `useEmulation: false`, so the test fails when real KVM
is unavailable instead of succeeding with slow QEMU emulation.

## Preflight on the Private Node

Run these checks on the physical or nested Linux node that will join the
provider VCI:

```bash
test -c /dev/kvm
ls -l /dev/kvm
grep -E -m1 '(vmx|svm)' /proc/cpuinfo
```

If the Private Node itself runs in a VM, its outer hypervisor must expose nested
virtualization and pass `/dev/kvm` through. On a containerized lab, the node
container also needs `--device=/dev/kvm` (or the equivalent runtime setting).

## Platform and VCI checks

After applying the sample, verify each boundary independently:

```bash
kubectl -n p-default get virtualclusterinstance local-kvm-provider -o yaml
kubectl get cluster local-kvm-provider -o yaml
kubectl -n p-default get argocdapplication local-kvm-provider-agent -o yaml
kubectl get nodeprovider local-kvm -o yaml
```

Connect to the provider VCI and check KubeVirt:

```bash
kubectl get nodes -o wide
kubectl -n kubevirt get pods
kubectl get kubevirt -A -o yaml
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.allocatable.devices\.kubevirt\.io/kvm}{"\n"}{end}'
```

At least one node must advertise `devices.kubevirt.io/kvm`. A running
`virt-handler` alone is not sufficient evidence.

## End-to-end proof

Create a second Private Node vCluster that uses the generated `local-kvm`
NodeProvider and `kvm-small` NodeType. Trigger one static or dynamic Auto Node,
then verify:

1. Platform creates a `NodeClaim`.
2. The provider VCI contains a KubeVirt `VirtualMachine` and running VMI.
3. The VM pod is scheduled on the KVM-capable Private Node.
4. The guest joins the consumer vCluster as a Ready Private Node.
5. Deleting the claim removes the VM, cloud-init Secret, and guest node.

This validates nested KVM passthrough and the complete control path. It does not
validate IOMMU isolation, VFIO reset, GPU identity, or secure GPU reclaim.

