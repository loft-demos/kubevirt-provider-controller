package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
