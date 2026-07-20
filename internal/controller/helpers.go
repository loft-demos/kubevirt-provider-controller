package controller

import (
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func parseConfig(obj *unstructured.Unstructured) (config, error) {
	name := obj.GetName()
	ns := obj.GetNamespace()
	get := func(path ...string) string {
		v, _, _ := unstructured.NestedString(obj.Object, append([]string{"spec"}, path...)...)
		return v
	}
	getBool := func(def bool, path ...string) bool {
		v, ok, _ := unstructured.NestedBool(obj.Object, append([]string{"spec"}, path...)...)
		if !ok {
			return def
		}
		return v
	}
	getInt := func(def int64, path ...string) int64 {
		v, ok, _ := unstructured.NestedInt64(obj.Object, append([]string{"spec"}, path...)...)
		if !ok {
			return def
		}
		return v
	}

	c := config{
		VCIName: get("virtualCluster", "name"), VCINamespace: get("virtualCluster", "namespace"),
		VCIClusterName:  get("virtualCluster", "clusterRef", "cluster"),
		VCITemplateName: get("virtualCluster", "templateRef", "name"), VCIParameters: get("virtualCluster", "parameters"),
		VCIOwnerUser: get("virtualCluster", "owner", "user"), VCIOwnerTeam: get("virtualCluster", "owner", "team"),
		DeleteVCI:            getBool(true, "virtualCluster", "deleteWithProviderCluster"),
		ConnectedClusterName: get("connectedCluster", "name"), ManagementNamespace: get("connectedCluster", "managementNamespace"),
		ArgoApplicationName: get("agent", "argoApplicationName"), AgentNamespace: get("agent", "namespace"),
		AgentSecretName: get("agent", "secretName"), AgentReleaseName: get("agent", "releaseName"),
		AgentChartRepo: get("agent", "chart", "repoURL"), AgentChartName: get("agent", "chart", "name"),
		AgentChartVersion: get("agent", "chart", "version"), KubeconfigTTLSeconds: getInt(600, "agent", "kubeconfigTTLSeconds"),
		NodeProviderName: get("nodeProvider", "name"), NodeProviderNamespace: get("nodeProvider", "targetNamespace"),
	}
	if c.VCIName == "" {
		c.VCIName = name
	}
	if c.VCINamespace == "" {
		c.VCINamespace = ns
	}
	if c.ConnectedClusterName == "" {
		c.ConnectedClusterName = name
	}
	if c.ManagementNamespace == "" {
		c.ManagementNamespace = "vcluster-platform"
	}
	if c.ArgoApplicationName == "" {
		c.ArgoApplicationName = name + "-agent"
	}
	if c.AgentNamespace == "" {
		c.AgentNamespace = "vcluster-platform"
	}
	if c.AgentSecretName == "" {
		c.AgentSecretName = "loft-agent-bootstrap"
	}
	if c.AgentReleaseName == "" {
		c.AgentReleaseName = "loft"
	}
	if c.AgentChartRepo == "" {
		c.AgentChartRepo = "https://charts.loft.sh"
	}
	if c.AgentChartName == "" {
		c.AgentChartName = "vcluster-platform"
	}
	if c.NodeProviderName == "" {
		c.NodeProviderName = name
	}
	if c.NodeProviderNamespace == "" {
		c.NodeProviderNamespace = "vcluster-platform"
	}
	tpl, _, _ := unstructured.NestedMap(obj.Object, "spec", "nodeProvider", "template")
	c.NodeProviderTemplate = tpl

	if c.VCITemplateName == "" {
		return c, fmt.Errorf("spec.virtualCluster.templateRef.name is required")
	}
	if (c.VCIOwnerUser == "") == (c.VCIOwnerTeam == "") {
		return c, fmt.Errorf("spec.virtualCluster.owner must set exactly one of user or team to a Platform resource name")
	}
	if c.AgentChartVersion == "" {
		return c, fmt.Errorf("spec.agent.chart.version is required and must match the Platform version")
	}
	if c.KubeconfigTTLSeconds < 60 || c.KubeconfigTTLSeconds > 3600 {
		return c, fmt.Errorf("spec.agent.kubeconfigTTLSeconds must be between 60 and 3600")
	}
	if c.AgentNamespace != c.ManagementNamespace {
		return c, fmt.Errorf("spec.agent.namespace must equal spec.connectedCluster.managementNamespace")
	}
	if len(c.NodeProviderTemplate) == 0 {
		return c, fmt.Errorf("spec.nodeProvider.template is required")
	}
	if _, found, _ := unstructured.NestedMap(c.NodeProviderTemplate, "spec", "kubeVirt"); !found {
		return c, fmt.Errorf("spec.nodeProvider.template.spec.kubeVirt is required")
	}
	return c, nil
}

func labelsFor(owner *unstructured.Unstructured) map[string]string {
	return map[string]string{managedByLabel: managedByValue, ownerNameLabel: owner.GetName(), ownerNamespaceLabel: owner.GetNamespace()}
}

func labelsObject(owner *unstructured.Unstructured) map[string]interface{} {
	out := map[string]interface{}{}
	for key, value := range labelsFor(owner) {
		out[key] = value
	}
	return out
}

func ownedBy(child, owner *unstructured.Unstructured) bool {
	labels := child.GetLabels()
	return labels[managedByLabel] == managedByValue && labels[ownerNameLabel] == owner.GetName() && labels[ownerNamespaceLabel] == owner.GetNamespace()
}

func ownershipError(kind string, child, owner *unstructured.Unstructured) error {
	if ownedBy(child, owner) {
		return nil
	}
	return fmt.Errorf("%s %q already exists and is not owned by %s/%s", kind, child.GetName(), owner.GetNamespace(), owner.GetName())
}

func conditionTrue(obj *unstructured.Unstructured, conditionType string) bool {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conditions {
		c, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if c["type"] == conditionType && strings.EqualFold(fmt.Sprint(c["status"]), "true") {
			return true
		}
	}
	return false
}

func argoApplicationReady(obj *unstructured.Unstructured) bool {
	return conditionTrue(obj, "Synced") &&
		nestedString(obj, "status", "application", "sync", "status") == "Synced" &&
		nestedString(obj, "status", "application", "health", "status") == "Healthy"
}

func setCondition(obj *unstructured.Unstructured, conditionType, status, reason, message string) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	now := metav1.Now().Format("2006-01-02T15:04:05Z")
	newCondition := map[string]interface{}{"type": conditionType, "status": status, "reason": reason, "message": message, "lastTransitionTime": now, "observedGeneration": obj.GetGeneration()}
	found := false
	for i, raw := range conditions {
		c, ok := raw.(map[string]interface{})
		if !ok || c["type"] != conditionType {
			continue
		}
		if c["status"] == status {
			newCondition["lastTransitionTime"] = c["lastTransitionTime"]
		}
		conditions[i] = newCondition
		found = true
		break
	}
	if !found {
		conditions = append(conditions, newCondition)
	}
	_ = unstructured.SetNestedSlice(obj.Object, conditions, "status", "conditions")
}

func toUnstructured(v interface{}) (*unstructured.Unstructured, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: m}, nil
}

func nestedString(obj *unstructured.Unstructured, path ...string) string {
	v, _, _ := unstructured.NestedString(obj.Object, path...)
	return v
}
