package controller

import "k8s.io/apimachinery/pkg/runtime/schema"

var (
	providerClusterGVR = schema.GroupVersionResource{Group: "infra.loft.sh", Version: "v1alpha1", Resource: "kubevirtproviderclusters"}
	vciGVR             = schema.GroupVersionResource{Group: "management.loft.sh", Version: "v1", Resource: "virtualclusterinstances"}
	clusterGVR         = schema.GroupVersionResource{Group: "management.loft.sh", Version: "v1", Resource: "clusters"}
	argoAppGVR         = schema.GroupVersionResource{Group: "management.loft.sh", Version: "v1", Resource: "argocdapplications"}
	nodeProviderGVR    = schema.GroupVersionResource{Group: "management.loft.sh", Version: "v1", Resource: "nodeproviders"}
)

const (
	finalizer           = "infra.loft.sh/kubevirt-provider-cluster"
	managedByLabel      = "app.kubernetes.io/managed-by"
	managedByValue      = "kubevirt-provider-controller"
	ownerNameLabel      = "infra.loft.sh/owner-name"
	ownerNamespaceLabel = "infra.loft.sh/owner-namespace"
)

type config struct {
	VCIName               string
	VCINamespace          string
	VCITemplateName       string
	VCIParameters         string
	VCIOwnerUser          string
	VCIOwnerTeam          string
	DeleteVCI             bool
	ConnectedClusterName  string
	ManagementNamespace   string
	ArgoApplicationName   string
	AgentNamespace        string
	AgentSecretName       string
	AgentReleaseName      string
	AgentChartRepo        string
	AgentChartName        string
	AgentChartVersion     string
	KubeconfigTTLSeconds  int64
	NodeProviderName      string
	NodeProviderNamespace string
	NodeProviderTemplate  map[string]interface{}
}
