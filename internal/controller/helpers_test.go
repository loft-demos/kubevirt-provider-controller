package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseConfigDefaults(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "infra.loft.sh/v1alpha1", "kind": "KubeVirtProviderCluster",
		"metadata": map[string]interface{}{"name": "provider-a", "namespace": "p-default"},
		"spec": map[string]interface{}{
			"virtualCluster": map[string]interface{}{"templateRef": map[string]interface{}{"name": "private-kvm"}},
			"agent":          map[string]interface{}{"chart": map[string]interface{}{"version": "4.11.0"}},
			"nodeProvider":   map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{"kubeVirt": map[string]interface{}{"nodeTypes": []interface{}{}}}}},
		},
	}}
	cfg, err := parseConfig(obj)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	checks := map[string][2]string{
		"vci name": {cfg.VCIName, "provider-a"}, "vci namespace": {cfg.VCINamespace, "p-default"},
		"cluster": {cfg.ConnectedClusterName, "provider-a"}, "agent namespace": {cfg.AgentNamespace, "vcluster-platform"},
		"secret": {cfg.AgentSecretName, "loft-agent-bootstrap"}, "provider": {cfg.NodeProviderName, "provider-a"},
	}
	for name, values := range checks {
		if values[0] != values[1] {
			t.Errorf("%s: got %q, want %q", name, values[0], values[1])
		}
	}
	if cfg.KubeconfigTTLSeconds != 600 {
		t.Errorf("TTL: got %d, want 600", cfg.KubeconfigTTLSeconds)
	}
}

func TestParseConfigRejectsUnsafeTTL(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "provider-a", "namespace": "p-default"},
		"spec": map[string]interface{}{
			"virtualCluster": map[string]interface{}{"templateRef": map[string]interface{}{"name": "private-kvm"}},
			"agent":          map[string]interface{}{"kubeconfigTTLSeconds": int64(7200), "chart": map[string]interface{}{"version": "4.11.0"}},
			"nodeProvider":   map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{}}},
		},
	}}
	if _, err := parseConfig(obj); err == nil {
		t.Fatal("expected unsafe TTL to be rejected")
	}
}

func TestConditionsAndOwnership(t *testing.T) {
	owner := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "a", "namespace": "p-default", "generation": int64(2)}}}
	child := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "child", "labels": labelsObject(owner)}}}
	if !ownedBy(child, owner) {
		t.Fatal("expected child ownership labels to match")
	}
	setCondition(owner, "Ready", "True", "Available", "ready")
	if !conditionTrue(owner, "Ready") {
		t.Fatal("expected Ready=True")
	}
}

func TestArgoApplicationReady(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]interface{}{"status": map[string]interface{}{
		"conditions":  []interface{}{map[string]interface{}{"type": "Synced", "status": "True"}},
		"application": map[string]interface{}{"sync": map[string]interface{}{"status": "Synced"}, "health": map[string]interface{}{"status": "Healthy"}},
	}}}
	if !argoApplicationReady(app) {
		t.Fatal("expected application to be ready")
	}
	_ = unstructured.SetNestedField(app.Object, "Progressing", "status", "application", "health", "status")
	if argoApplicationReady(app) {
		t.Fatal("expected progressing application not to be ready")
	}
}
