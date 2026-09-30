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

	// Platform tenancy labels (loft-enterprise storagev1.TenantLabel and
	// storagev1.TenantExclusiveHolderLabel). A resource carries at most one.
	tenantOwnerLabel     = "tenant.vcluster.com/owner"
	tenantExclusiveLabel = "tenant.vcluster.com/exclusive-to"

	// capacityTypeProperty selects reserved and/or on-demand KubeVirt placement
	// for a NodeType (loft-enterprise capacity.TypeProperty).
	capacityTypeProperty = "kubevirt.vcluster.com/capacity-type"
)

const (
	tenantAssignmentOwned     = "Owned"
	tenantAssignmentExclusive = "Exclusive"

	capacityTypeReserved = "reserved"
	capacityTypeOnDemand = "on-demand"
)

type config struct {
	VCIName               string
	VCINamespace          string
	VCIClusterName        string
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
	// CapacityTypes is stamped onto every NodeType that does not set its own
	// capacity-type property. Empty leaves Platform's on-demand default.
	CapacityTypes    []string
	TenantName       string
	TenantAssignment string
}
