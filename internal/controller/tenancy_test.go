package controller

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

func providerClusterWith(spec map[string]interface{}) *unstructured.Unstructured {
	base := map[string]interface{}{
		"virtualCluster": map[string]interface{}{"templateRef": map[string]interface{}{"name": "private-kvm"}, "owner": map[string]interface{}{"user": "admin"}},
		"agent":          map[string]interface{}{"chart": map[string]interface{}{"version": "4.13.0"}},
		"nodeProvider":   map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{"kubeVirt": map[string]interface{}{"nodeTypes": []interface{}{}}}}},
	}
	for key, value := range spec {
		if key == "nodeProvider" {
			for k, v := range value.(map[string]interface{}) {
				base["nodeProvider"].(map[string]interface{})[k] = v
			}
			continue
		}
		base[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "infra.loft.sh/v1alpha1", "kind": "KubeVirtProviderCluster",
		"metadata": map[string]interface{}{"name": "provider-a", "namespace": "p-default"},
		"spec":     base,
	}}
}

func TestParseConfigTenantDefaultsToExclusive(t *testing.T) {
	cfg, err := parseConfig(providerClusterWith(map[string]interface{}{
		"tenant":       map[string]interface{}{"name": "lab"},
		"nodeProvider": map[string]interface{}{"capacityTypes": []interface{}{"reserved", "on-demand"}},
	}))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.TenantName != "lab" || cfg.TenantAssignment != tenantAssignmentExclusive {
		t.Fatalf("tenant: got name=%q assignment=%q, want lab/%s", cfg.TenantName, cfg.TenantAssignment, tenantAssignmentExclusive)
	}
	if strings.Join(cfg.CapacityTypes, ",") != "reserved,on-demand" {
		t.Fatalf("capacity types: got %v", cfg.CapacityTypes)
	}
}

func TestParseConfigRejectsInvalidTenancy(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"unknown capacity type":   {"nodeProvider": map[string]interface{}{"capacityTypes": []interface{}{"spot"}}},
		"assignment without name": {"tenant": map[string]interface{}{"assignment": "Owned"}},
		"unknown assignment":      {"tenant": map[string]interface{}{"name": "lab", "assignment": "Shared"}},
	}
	for name, spec := range cases {
		if _, err := parseConfig(providerClusterWith(spec)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDesiredNodeProviderStampsTenantAndCapacityTypes(t *testing.T) {
	owner := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "provider-a", "namespace": "p-platform"}}}
	cfg := config{
		ConnectedClusterName: "provider-a", NodeProviderName: "provider-a", NodeProviderNamespace: "vcluster-platform",
		TenantName: "lab", TenantAssignment: tenantAssignmentOwned, CapacityTypes: []string{"reserved", "on-demand"},
		NodeProviderTemplate: map[string]interface{}{
			"metadata": map[string]interface{}{"labels": map[string]interface{}{"ignored": "true"}},
			"spec": map[string]interface{}{"kubeVirt": map[string]interface{}{"nodeTypes": []interface{}{
				map[string]interface{}{"name": "kvm-small"},
				map[string]interface{}{"name": "kvm-shared", "properties": map[string]interface{}{capacityTypeProperty: "on-demand", "other": "kept"}},
			}}},
		},
	}
	desired, err := desiredNodeProvider(owner, cfg)
	if err != nil {
		t.Fatal(err)
	}
	labels := desired.GetLabels()
	if labels[tenantOwnerLabel] != "lab" {
		t.Fatalf("tenant owner label: got %q, want lab", labels[tenantOwnerLabel])
	}
	if _, found := labels[tenantExclusiveLabel]; found {
		t.Fatal("an Owned provider must not also carry the exclusive-to label")
	}
	if _, found := labels["ignored"]; found {
		t.Fatal("template labels must not reach the NodeProvider")
	}
	if !ownedBy(desired, owner) {
		t.Fatal("desired NodeProvider lost its controller ownership labels")
	}

	nodeTypes, _, _ := unstructured.NestedSlice(desired.Object, "spec", "kubeVirt", "nodeTypes")
	small, _, _ := unstructured.NestedStringMap(nodeTypes[0].(map[string]interface{}), "properties")
	if small[capacityTypeProperty] != "reserved,on-demand" {
		t.Fatalf("kvm-small capacity type: got %q", small[capacityTypeProperty])
	}
	shared, _, _ := unstructured.NestedStringMap(nodeTypes[1].(map[string]interface{}), "properties")
	if shared[capacityTypeProperty] != "on-demand" || shared["other"] != "kept" {
		t.Fatalf("kvm-shared properties were overwritten: %v", shared)
	}

	// The template itself must stay untouched for the next reconcile.
	original, _, _ := unstructured.NestedSlice(cfg.NodeProviderTemplate, "spec", "kubeVirt", "nodeTypes")
	if _, found := original[0].(map[string]interface{})["properties"]; found {
		t.Fatal("stamping capacity types mutated the KubeVirtProviderCluster template")
	}
}

func TestMergeNodeProviderLabels(t *testing.T) {
	current := map[string]string{
		managedByLabel:                     managedByValue,
		"scope.tenant.vcluster.com/lab":    "true",
		tenantExclusiveLabel:               "old-tenant",
		"platform.example/annotated-label": "kept",
	}
	desired := map[string]string{managedByLabel: managedByValue, tenantOwnerLabel: "lab"}
	got := mergeNodeProviderLabels(current, desired)
	if got["scope.tenant.vcluster.com/lab"] != "true" || got["platform.example/annotated-label"] != "kept" {
		t.Fatalf("labels written by others were dropped: %v", got)
	}
	if _, found := got[tenantExclusiveLabel]; found {
		t.Fatalf("stale exclusive-to label survived: %v", got)
	}
	if got[tenantOwnerLabel] != "lab" {
		t.Fatalf("tenant owner label: got %q", got[tenantOwnerLabel])
	}
}

func TestOwnerKey(t *testing.T) {
	key, ok := ownerKey(map[string]string{managedByLabel: managedByValue, ownerNamespaceLabel: "p-platform", ownerNameLabel: "provider-a"})
	if !ok || key != "p-platform/provider-a" {
		t.Fatalf("ownerKey: got %q, %v", key, ok)
	}
	if _, ok := ownerKey(map[string]string{managedByLabel: "someone-else", ownerNamespaceLabel: "p-platform", ownerNameLabel: "provider-a"}); ok {
		t.Fatal("ownerKey must ignore resources managed by another controller")
	}
}

func TestEnqueueOwnerFromChildAndTombstone(t *testing.T) {
	c := &Controller{queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "test")}
	defer c.queue.ShutDown()
	child := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{
		"name": "provider-a", "labels": map[string]interface{}{managedByLabel: managedByValue, ownerNamespaceLabel: "p-platform", ownerNameLabel: "provider-a"},
	}}}
	c.enqueueOwner(child)
	c.enqueueOwner(cache.DeletedFinalStateUnknown{Key: "provider-a", Obj: child})
	if c.queue.Len() != 1 {
		t.Fatalf("queue length: got %d, want 1 deduplicated owner key", c.queue.Len())
	}
	item, _ := c.queue.Get()
	if item != "p-platform/provider-a" {
		t.Fatalf("queued key: got %v", item)
	}
}
