package controller

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

func TestDesiredResourcesKeepEnrollmentTokenOutOfArgo(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		vciGVR: "VirtualClusterInstanceList", clusterGVR: "ClusterList", argoAppGVR: "ArgoCDApplicationList", nodeProviderGVR: "NodeProviderList",
	})
	c := &Controller{dynamic: client}
	owner := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "infra.loft.sh/v1alpha1", "kind": "KubeVirtProviderCluster",
		"metadata": map[string]interface{}{"name": "provider-a", "namespace": "p-default"},
		"status":   map[string]interface{}{"agentConnection": map[string]interface{}{"caCert": "base64-ca", "insecure": false}},
	}}
	cfg := config{
		VCIName: "provider-a", VCINamespace: "p-default", VCITemplateName: "private-kvm",
		VCIOwnerUser:         "admin",
		ConnectedClusterName: "provider-a", ManagementNamespace: "vcluster-platform",
		ArgoApplicationName: "provider-a-agent", AgentNamespace: "vcluster-platform", AgentSecretName: "bootstrap-secret",
		AgentReleaseName: "loft", AgentChartRepo: "https://charts.loft.sh", AgentChartName: "vcluster-platform", AgentChartVersion: "test-version",
		NodeProviderName: "provider-a", NodeProviderNamespace: "vcluster-platform",
		NodeProviderTemplate: map[string]interface{}{"spec": map[string]interface{}{"kubeVirt": map[string]interface{}{"clusterRef": map[string]interface{}{}, "nodeTypes": []interface{}{}}}},
	}
	ctx := context.Background()
	if err := c.ensureVCI(ctx, owner, cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureCluster(ctx, owner, cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureArgoApplication(ctx, owner, cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureNodeProvider(ctx, owner, cfg); err != nil {
		t.Fatal(err)
	}

	cluster, err := client.Resource(clusterGVR).Get(ctx, "provider-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	unusable, _, _ := unstructured.NestedBool(cluster.Object, "spec", "unusable")
	if !unusable {
		t.Fatal("connected provider Cluster must remain unusable")
	}
	vci, err := client.Resource(vciGVR).Namespace("p-default").Get(ctx, "provider-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := nestedString(vci, "spec", "owner", "user"); got != "admin" {
		t.Fatalf("VCI owner user: got %q, want admin", got)
	}

	app, err := client.Resource(argoAppGVR).Namespace("p-default").Get(ctx, "provider-a-agent", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	values := nestedString(app, "spec", "template", "spec", "source", "helm", "values")
	if !strings.Contains(values, "tokenSecretRef:\n  name: bootstrap-secret") {
		t.Fatalf("agent values do not reference bootstrap Secret:\n%s", values)
	}
	if strings.Contains(values, "access-key-value") || strings.Contains(values, "token:") {
		t.Fatalf("agent token was inlined into Argo values:\n%s", values)
	}
	if got := nestedString(app, "spec", "destination", "virtualCluster", "namespace"); got != "" {
		t.Fatalf("destination virtualCluster namespace: got %q, want empty", got)
	}
	if got := nestedString(app, "spec", "template", "spec", "destination", "namespace"); got != "vcluster-platform" {
		t.Fatalf("template destination namespace: got %q, want vcluster-platform", got)
	}

	np, err := client.Resource(nodeProviderGVR).Get(ctx, "provider-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := nestedString(np, "spec", "kubeVirt", "clusterRef", "cluster"); got != "provider-a" {
		t.Fatalf("clusterRef.cluster: got %q", got)
	}
	if got := nestedString(np, "spec", "kubeVirt", "clusterRef", "namespace"); got != "vcluster-platform" {
		t.Fatalf("clusterRef.namespace: got %q", got)
	}
}

func TestEnsureVCIRepairsManagedOwner(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "VirtualClusterInstance",
		"metadata": map[string]interface{}{
			"name": "provider-a", "namespace": "p-default",
			"labels": map[string]interface{}{managedByLabel: managedByValue, ownerNameLabel: "provider-a", ownerNamespaceLabel: "p-default"},
		},
		"spec": map[string]interface{}{"templateRef": map[string]interface{}{"name": "private-kvm"}},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), existing)
	c := &Controller{dynamic: client}
	owner := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "provider-a", "namespace": "p-default"}}}
	cfg := config{VCIName: "provider-a", VCINamespace: "p-default", VCITemplateName: "private-kvm", VCIOwnerTeam: "platform-admins"}
	if err := c.ensureVCI(context.Background(), owner, cfg); err != nil {
		t.Fatal(err)
	}
	updated, err := client.Resource(vciGVR).Namespace("p-default").Get(context.Background(), "provider-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := nestedString(updated, "spec", "owner", "team"); got != "platform-admins" {
		t.Fatalf("VCI owner team: got %q, want platform-admins", got)
	}
}

func TestEnsureNodeProviderUpdatesManagedSpec(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "NodeProvider",
		"metadata": map[string]interface{}{
			"name": "provider-a",
			"labels": map[string]interface{}{
				managedByLabel:      managedByValue,
				ownerNameLabel:      "provider-a",
				ownerNamespaceLabel: "p-platform",
			},
		},
		"spec": map[string]interface{}{"kubeVirt": map[string]interface{}{
			"clusterRef": map[string]interface{}{"cluster": "provider-a", "namespace": "vcluster-platform"},
			"deploy": map[string]interface{}{"kubevirt": map[string]interface{}{
				"enabled":    true,
				"helmValues": "kubevirt:\n  spec:\n    configuration:\n      developerConfiguration:\n        useEmulation: false\n",
			}},
		}},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), existing)
	c := &Controller{dynamic: client}
	owner := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "provider-a", "namespace": "p-platform"}}}
	cfg := config{
		ConnectedClusterName: "provider-a", NodeProviderName: "provider-a", NodeProviderNamespace: "vcluster-platform",
		NodeProviderTemplate: map[string]interface{}{"spec": map[string]interface{}{"kubeVirt": map[string]interface{}{
			"clusterRef": map[string]interface{}{},
			"deploy": map[string]interface{}{"kubevirt": map[string]interface{}{
				"enabled":    true,
				"helmValues": "kubevirt:\n  operator:\n    affinity:\n      nodeAffinity: null\n  spec:\n    configuration:\n      developerConfiguration:\n        useEmulation: false\n",
			}},
		}}},
	}
	if err := c.ensureNodeProvider(context.Background(), owner, cfg); err != nil {
		t.Fatal(err)
	}
	updated, err := client.Resource(nodeProviderGVR).Get(context.Background(), "provider-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	values := nestedString(updated, "spec", "kubeVirt", "deploy", "kubevirt", "helmValues")
	if !strings.Contains(values, "nodeAffinity: null") {
		t.Fatalf("helm values were not updated:\n%s", values)
	}
	if got := nestedString(updated, "spec", "kubeVirt", "clusterRef", "cluster"); got != "provider-a" {
		t.Fatalf("clusterRef.cluster: got %q, want provider-a", got)
	}
}

func TestRefusesToAdoptExistingResource(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "Cluster", "metadata": map[string]interface{}{"name": "provider-a"},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), existing)
	c := &Controller{dynamic: client}
	owner := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "provider-a", "namespace": "p-default"}}}
	err := c.ensureCluster(context.Background(), owner, config{ConnectedClusterName: "provider-a", ManagementNamespace: "vcluster-platform"})
	if err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("expected ownership error, got %v", err)
	}
}

func TestReconcileDoesNotGateEnrollmentOnAggregateArgoCondition(t *testing.T) {
	provider := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "infra.loft.sh/v1alpha1", "kind": "KubeVirtProviderCluster",
		"metadata": map[string]interface{}{
			"name": "provider-a", "namespace": "p-default", "generation": int64(1),
			"finalizers": []interface{}{finalizer},
		},
		"spec": map[string]interface{}{
			"virtualCluster": map[string]interface{}{
				"name": "provider-a", "templateRef": map[string]interface{}{"name": "private-kvm"},
				"owner": map[string]interface{}{"user": "admin"},
			},
			"agent": map[string]interface{}{"chart": map[string]interface{}{"version": "4.11.0"}},
			"nodeProvider": map[string]interface{}{"template": map[string]interface{}{
				"spec": map[string]interface{}{"kubeVirt": map[string]interface{}{}},
			}},
		},
	}}
	vci := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "VirtualClusterInstance",
		"metadata": map[string]interface{}{
			"name": "provider-a", "namespace": "p-default",
			"labels": map[string]interface{}{managedByLabel: managedByValue, ownerNameLabel: "provider-a", ownerNamespaceLabel: "p-default"},
		},
		"spec": map[string]interface{}{"owner": map[string]interface{}{"user": "admin"}},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "VirtualClusterReady", "status": "True"},
			map[string]interface{}{"type": "VirtualClusterOnline", "status": "True"},
			map[string]interface{}{"type": "ArgoCDIntegrationSynced", "status": "False", "reason": "UnrelatedApplicationFailed"},
		}},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), provider, vci)
	platform, err := newPlatformClient(&rest.Config{
		Host: "https://platform.test/kubernetes/management",
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"message":"not ready"}`)),
				Request:    r,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &Controller{dynamic: client, platform: platform}
	if err := c.reconcile(context.Background(), "p-default", "provider-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(clusterGVR).Get(context.Background(), "provider-a", metav1.GetOptions{}); err != nil {
		t.Fatalf("connected Cluster was not created: %v", err)
	}
	updated, err := client.Resource(providerClusterGVR).Namespace("p-default").Get(context.Background(), "provider-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := nestedStringFromCondition(updated, "EnrollmentSecretStaged", "reason"); got != "EnrollmentFailed" {
		t.Fatalf("enrollment condition reason: got %q, want EnrollmentFailed", got)
	}
}

func TestReconcileRestagesEnrollmentWhenConditionIsStale(t *testing.T) {
	provider := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "infra.loft.sh/v1alpha1", "kind": "KubeVirtProviderCluster",
		"metadata": map[string]interface{}{
			"name": "provider-a", "namespace": "p-platform", "generation": int64(4),
			"finalizers": []interface{}{finalizer},
		},
		"spec": map[string]interface{}{
			"virtualCluster": map[string]interface{}{
				"name": "provider-a", "templateRef": map[string]interface{}{"name": "private-kvm"},
				"owner": map[string]interface{}{"user": "admin"},
			},
			"agent": map[string]interface{}{"chart": map[string]interface{}{"version": "4.11.0"}},
			"nodeProvider": map[string]interface{}{"template": map[string]interface{}{
				"spec": map[string]interface{}{"kubeVirt": map[string]interface{}{}},
			}},
		},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "EnrollmentSecretStaged", "status": "True", "reason": "Staged", "observedGeneration": int64(1)},
		}},
	}}
	vci := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "VirtualClusterInstance",
		"metadata": map[string]interface{}{
			"name": "provider-a", "namespace": "p-platform",
			"labels": map[string]interface{}{managedByLabel: managedByValue, ownerNameLabel: "provider-a", ownerNamespaceLabel: "p-platform"},
		},
		"spec": map[string]interface{}{"owner": map[string]interface{}{"user": "admin"}},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "VirtualClusterReady", "status": "True"},
			map[string]interface{}{"type": "VirtualClusterOnline", "status": "True"},
		}},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), provider, vci)
	platform, err := newPlatformClient(&rest.Config{
		Host: "https://platform.test/kubernetes/management",
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"message":"not ready"}`)),
				Request:    r,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &Controller{dynamic: client, platform: platform}
	if err := c.reconcile(context.Background(), "p-platform", "provider-a"); err != nil {
		t.Fatal(err)
	}
	updated, err := client.Resource(providerClusterGVR).Namespace("p-platform").Get(context.Background(), "provider-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := nestedStringFromCondition(updated, "EnrollmentSecretStaged", "reason"); got != "EnrollmentFailed" {
		t.Fatalf("enrollment condition reason: got %q, want EnrollmentFailed", got)
	}
	if _, err := client.Resource(argoAppGVR).Namespace("p-platform").Get(context.Background(), "provider-a-agent", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("agent ArgoCDApplication should not be reconciled before restaging enrollment, got err %v", err)
	}
}

func nestedStringFromCondition(obj *unstructured.Unstructured, conditionType, field string) string {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]interface{})
		if ok && condition["type"] == conditionType {
			return condition[field].(string)
		}
	}
	return ""
}
