package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

type Controller struct {
	dynamic  dynamic.Interface
	platform *platformClient
	informer cache.SharedIndexInformer
	queue    workqueue.RateLimitingInterface
}

func New(cfg *rest.Config, dynamicClient dynamic.Interface, resync time.Duration) (*Controller, error) {
	platform, err := newPlatformClient(cfg)
	if err != nil {
		return nil, err
	}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynamicClient, resync, metav1.NamespaceAll, nil)
	informer := factory.ForResource(providerClusterGVR).Informer()
	c := &Controller{dynamic: dynamicClient, platform: platform, informer: informer,
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kubevirt-provider-clusters")}
	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: c.enqueue, UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) }, DeleteFunc: c.enqueue,
	})
	return c, err
}

func SignalContext() context.Context {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() { <-ctx.Done(); cancel() }()
	return ctx
}

func (c *Controller) Run(ctx context.Context, workers int) error {
	defer c.queue.ShutDown()
	go c.informer.Run(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), c.informer.HasSynced) {
		return errors.New("timed out waiting for informer cache sync")
	}
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.worker, time.Second)
	}
	klog.InfoS("controller started", "workers", workers)
	<-ctx.Done()
	return nil
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		klog.ErrorS(err, "create queue key")
		return
	}
	c.queue.Add(key)
}

func (c *Controller) worker(ctx context.Context) {
	for c.processNext(ctx) {
	}
}

func (c *Controller) processNext(ctx context.Context) bool {
	item, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(item)
	key, ok := item.(string)
	if !ok {
		c.queue.Forget(item)
		return true
	}
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		c.queue.Forget(item)
		return true
	}
	err = c.reconcile(ctx, ns, name)
	if err != nil {
		klog.ErrorS(err, "reconcile failed", "namespace", ns, "name", name)
		c.queue.AddRateLimited(key)
	} else {
		c.queue.Forget(item)
	}
	return true
}

func (c *Controller) reconcile(ctx context.Context, namespace, name string) error {
	obj, err := c.dynamic.Resource(providerClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := parseConfig(obj)
	if err != nil {
		return c.fail(ctx, obj, "InvalidSpec", err)
	}
	if obj.GetDeletionTimestamp() != nil {
		return c.finalize(ctx, obj, cfg)
	}
	if !contains(obj.GetFinalizers(), finalizer) {
		copy := obj.DeepCopy()
		copy.SetFinalizers(append(copy.GetFinalizers(), finalizer))
		_, err = c.dynamic.Resource(providerClusterGVR).Namespace(namespace).Update(ctx, copy, metav1.UpdateOptions{})
		return err
	}

	if err := c.ensureVCI(ctx, obj, cfg); err != nil {
		return c.progress(ctx, obj, "VirtualClusterReady", "False", "CreatingVirtualCluster", err.Error())
	}
	vci, err := c.dynamic.Resource(vciGVR).Namespace(cfg.VCINamespace).Get(ctx, cfg.VCIName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !conditionTrue(vci, "VirtualClusterReady") || !conditionTrue(vci, "VirtualClusterOnline") {
		return c.progress(ctx, obj, "VirtualClusterReady", "False", "WaitingForVirtualCluster", "waiting for the Private Node vCluster to be ready and online")
	}
	if err := c.ensureCluster(ctx, obj, cfg); err != nil {
		return err
	}
	if !conditionTrue(obj, "EnrollmentSecretStaged") {
		if err := c.stageEnrollment(ctx, obj, cfg); err != nil {
			return c.progress(ctx, obj, "EnrollmentSecretStaged", "False", "EnrollmentFailed", err.Error())
		}
		obj, err = c.dynamic.Resource(providerClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := c.progress(ctx, obj, "EnrollmentSecretStaged", "True", "Staged", "agent credentials were written through the Platform proxy using a short-lived token kubeconfig"); err != nil {
			return err
		}
		return nil
	}
	if err := c.ensureArgoApplication(ctx, obj, cfg); err != nil {
		return err
	}
	app, err := c.dynamic.Resource(argoAppGVR).Namespace(cfg.VCINamespace).Get(ctx, cfg.ArgoApplicationName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !argoApplicationReady(app) {
		return c.progress(ctx, obj, "AgentApplicationReady", "False", "WaitingForArgoCD", "waiting for Argo CD to sync the vCluster Platform agent")
	}
	cluster, err := c.dynamic.Resource(clusterGVR).Get(ctx, cfg.ConnectedClusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if nestedString(cluster, "status", "phase") != "Initialized" {
		return c.progress(ctx, obj, "ConnectedClusterReady", "False", "WaitingForAgent", "Argo CD is synced; waiting for the connected cluster agent to initialize")
	}
	if err := c.ensureNodeProvider(ctx, obj, cfg); err != nil {
		return err
	}
	np, err := c.dynamic.Resource(nodeProviderGVR).Get(ctx, cfg.NodeProviderName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if nestedString(np, "status", "phase") != "Available" {
		return c.progress(ctx, obj, "Ready", "False", "WaitingForNodeProvider", "connected cluster is initialized; waiting for the KubeVirt NodeProvider")
	}
	return c.ready(ctx, obj, cfg)
}

func (c *Controller) ensureVCI(ctx context.Context, owner *unstructured.Unstructured, cfg config) error {
	existing, err := c.dynamic.Resource(vciGVR).Namespace(cfg.VCINamespace).Get(ctx, cfg.VCIName, metav1.GetOptions{})
	if err == nil {
		if err := ownershipError("VirtualClusterInstance", existing, owner); err != nil {
			return err
		}
		return c.ensureVCIOwner(ctx, existing, cfg)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	spec := map[string]interface{}{
		"templateRef": map[string]interface{}{"name": cfg.VCITemplateName},
		"parameters":  cfg.VCIParameters,
		"owner":       vciOwner(cfg),
	}
	vci := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "VirtualClusterInstance",
		"metadata": map[string]interface{}{"name": cfg.VCIName, "namespace": cfg.VCINamespace, "labels": labelsObject(owner)},
		"spec":     spec,
	}}
	_, err = c.dynamic.Resource(vciGVR).Namespace(cfg.VCINamespace).Create(ctx, vci, metav1.CreateOptions{})
	if err == nil {
		klog.InfoS("created VirtualClusterInstance", "namespace", cfg.VCINamespace, "name", cfg.VCIName)
	}
	return err
}

func (c *Controller) ensureVCIOwner(ctx context.Context, vci *unstructured.Unstructured, cfg config) error {
	desired := vciOwner(cfg)
	current, _, _ := unstructured.NestedMap(vci.Object, "spec", "owner")
	if reflect.DeepEqual(current, desired) {
		return nil
	}
	copy := vci.DeepCopy()
	if err := unstructured.SetNestedMap(copy.Object, desired, "spec", "owner"); err != nil {
		return err
	}
	_, err := c.dynamic.Resource(vciGVR).Namespace(cfg.VCINamespace).Update(ctx, copy, metav1.UpdateOptions{})
	if err == nil {
		klog.InfoS("updated VirtualClusterInstance owner", "namespace", cfg.VCINamespace, "name", cfg.VCIName)
	}
	return err
}

func vciOwner(cfg config) map[string]interface{} {
	if cfg.VCIOwnerUser != "" {
		return map[string]interface{}{"user": cfg.VCIOwnerUser}
	}
	return map[string]interface{}{"team": cfg.VCIOwnerTeam}
}

func (c *Controller) ensureCluster(ctx context.Context, owner *unstructured.Unstructured, cfg config) error {
	existing, err := c.dynamic.Resource(clusterGVR).Get(ctx, cfg.ConnectedClusterName, metav1.GetOptions{})
	if err == nil {
		return ownershipError("Cluster", existing, owner)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "Cluster", "metadata": map[string]interface{}{"name": cfg.ConnectedClusterName, "labels": labelsObject(owner)},
		"spec": map[string]interface{}{"displayName": cfg.ConnectedClusterName + " (KubeVirt provider)", "description": "Private Node vCluster dedicated as a KubeVirt control-plane cluster", "networkPeer": true, "unusable": true, "managementNamespace": cfg.ManagementNamespace},
	}}
	_, err = c.dynamic.Resource(clusterGVR).Create(ctx, cluster, metav1.CreateOptions{})
	if err == nil {
		klog.InfoS("created connected Cluster", "name", cfg.ConnectedClusterName)
	}
	return err
}

func (c *Controller) stageEnrollment(ctx context.Context, owner *unstructured.Unstructured, cfg config) error {
	access, err := c.platform.clusterAccessKey(ctx, cfg.ConnectedClusterName)
	if err != nil {
		return fmt.Errorf("get connected-cluster access key: %w", err)
	}
	kubeconfig, err := c.platform.virtualClusterKubeconfig(ctx, cfg.VCINamespace, cfg.VCIName, cfg.KubeconfigTTLSeconds)
	if err != nil {
		return fmt.Errorf("get temporary vCluster kubeconfig: %w", err)
	}
	if err := stageAgentSecret(ctx, kubeconfig, cfg.AgentNamespace, cfg.AgentSecretName, access, labelsFor(owner)); err != nil {
		return fmt.Errorf("stage agent Secret: %w", err)
	}
	return c.recordAgentTLS(ctx, owner, access)
}

func (c *Controller) recordAgentTLS(ctx context.Context, owner *unstructured.Unstructured, access agentAccess) error {
	copy := owner.DeepCopy()
	_ = unstructured.SetNestedField(copy.Object, access.CACert, "status", "agentConnection", "caCert")
	_ = unstructured.SetNestedField(copy.Object, access.Insecure, "status", "agentConnection", "insecure")
	_, err := c.updateStatus(ctx, owner, copy)
	return err
}

func (c *Controller) ensureArgoApplication(ctx context.Context, owner *unstructured.Unstructured, cfg config) error {
	existing, err := c.dynamic.Resource(argoAppGVR).Namespace(cfg.VCINamespace).Get(ctx, cfg.ArgoApplicationName, metav1.GetOptions{})
	if err == nil {
		return ownershipError("ArgoCDApplication", existing, owner)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	ca, _, _ := unstructured.NestedString(owner.Object, "status", "agentConnection", "caCert")
	insecure, _, _ := unstructured.NestedBool(owner.Object, "status", "agentConnection", "insecure")
	values := fmt.Sprintf("agentOnly: true\ntokenSecretRef:\n  name: %s\ninsecureSkipVerify: %t\n", cfg.AgentSecretName, insecure)
	if ca != "" && !insecure {
		values += "additionalCA: " + ca + "\n"
	}
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "ArgoCDApplication", "metadata": map[string]interface{}{"name": cfg.ArgoApplicationName, "namespace": cfg.VCINamespace, "labels": labelsObject(owner)},
		"spec": map[string]interface{}{
			"displayName": cfg.ConnectedClusterName + " agent", "destination": map[string]interface{}{"virtualCluster": map[string]interface{}{"name": cfg.VCIName, "namespace": cfg.AgentNamespace, "target": "vCluster"}},
			"template": map[string]interface{}{"spec": map[string]interface{}{"project": "default", "source": map[string]interface{}{"repoURL": cfg.AgentChartRepo, "chart": cfg.AgentChartName, "targetRevision": cfg.AgentChartVersion, "helm": map[string]interface{}{"releaseName": cfg.AgentReleaseName, "values": values}}, "syncPolicy": map[string]interface{}{"automated": map[string]interface{}{"prune": true, "selfHeal": true}, "syncOptions": []interface{}{"CreateNamespace=true"}}}},
		},
	}}
	_, err = c.dynamic.Resource(argoAppGVR).Namespace(cfg.VCINamespace).Create(ctx, app, metav1.CreateOptions{})
	if err == nil {
		klog.InfoS("created agent ArgoCDApplication", "namespace", cfg.VCINamespace, "name", cfg.ArgoApplicationName)
	}
	return err
}

func (c *Controller) ensureNodeProvider(ctx context.Context, owner *unstructured.Unstructured, cfg config) error {
	existing, err := c.dynamic.Resource(nodeProviderGVR).Get(ctx, cfg.NodeProviderName, metav1.GetOptions{})
	if err == nil {
		return ownershipError("NodeProvider", existing, owner)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	tpl := runtime.DeepCopyJSON(cfg.NodeProviderTemplate)
	tpl["apiVersion"] = "management.loft.sh/v1"
	tpl["kind"] = "NodeProvider"
	meta, _, _ := unstructured.NestedMap(tpl, "metadata")
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["name"] = cfg.NodeProviderName
	meta["labels"] = labelsObject(owner)
	tpl["metadata"] = meta
	if err := unstructured.SetNestedMap(tpl, map[string]interface{}{"cluster": cfg.ConnectedClusterName, "namespace": cfg.NodeProviderNamespace}, "spec", "kubeVirt", "clusterRef"); err != nil {
		return err
	}
	_, err = c.dynamic.Resource(nodeProviderGVR).Create(ctx, &unstructured.Unstructured{Object: tpl}, metav1.CreateOptions{})
	if err == nil {
		klog.InfoS("created KubeVirt NodeProvider", "name", cfg.NodeProviderName, "cluster", cfg.ConnectedClusterName)
	}
	return err
}

func (c *Controller) ready(ctx context.Context, obj *unstructured.Unstructured, cfg config) error {
	copy := obj.DeepCopy()
	setCondition(copy, "VirtualClusterReady", "True", "Ready", "Private Node vCluster is ready and online")
	setCondition(copy, "ArgoCDReady", "True", "Ready", "agent application is synced through the v2 Argo CD integration")
	setCondition(copy, "AgentApplicationReady", "True", "Synced", "agent application is synced by Argo CD")
	setCondition(copy, "ConnectedClusterReady", "True", "Initialized", "vCluster is connected as a dedicated, unusable Platform cluster")
	setCondition(copy, "Ready", "True", "Available", "KubeVirt NodeProvider is available")
	_ = unstructured.SetNestedField(copy.Object, cfg.VCIName, "status", "resources", "virtualCluster")
	_ = unstructured.SetNestedField(copy.Object, cfg.ConnectedClusterName, "status", "resources", "connectedCluster")
	_ = unstructured.SetNestedField(copy.Object, cfg.ArgoApplicationName, "status", "resources", "argoCDApplication")
	_ = unstructured.SetNestedField(copy.Object, cfg.NodeProviderName, "status", "resources", "nodeProvider")
	changed, err := c.updateStatus(ctx, obj, copy)
	if err == nil && changed {
		klog.InfoS("KubeVirt provider cluster is ready", "namespace", obj.GetNamespace(), "name", obj.GetName(), "nodeProvider", cfg.NodeProviderName)
	}
	return err
}

func (c *Controller) progress(ctx context.Context, obj *unstructured.Unstructured, typ, status, reason, message string) error {
	copy := obj.DeepCopy()
	setCondition(copy, typ, status, reason, message)
	if typ != "Ready" {
		setCondition(copy, "Ready", "False", reason, message)
	}
	changed, err := c.updateStatus(ctx, obj, copy)
	if err == nil && changed {
		klog.InfoS("reconciliation state changed", "namespace", obj.GetNamespace(), "name", obj.GetName(), "condition", typ, "status", status, "reason", reason, "message", message)
	}
	return err
}

func (c *Controller) updateStatus(ctx context.Context, before, after *unstructured.Unstructured) (bool, error) {
	oldStatus, _, _ := unstructured.NestedMap(before.Object, "status")
	newStatus, _, _ := unstructured.NestedMap(after.Object, "status")
	if reflect.DeepEqual(oldStatus, newStatus) {
		return false, nil
	}
	_, err := c.dynamic.Resource(providerClusterGVR).Namespace(after.GetNamespace()).UpdateStatus(ctx, after, metav1.UpdateOptions{})
	return err == nil, err
}

func (c *Controller) fail(ctx context.Context, obj *unstructured.Unstructured, reason string, cause error) error {
	if err := c.progress(ctx, obj, "Ready", "False", reason, cause.Error()); err != nil {
		return err
	}
	return nil
}

func (c *Controller) finalize(ctx context.Context, obj *unstructured.Unstructured, cfg config) error {
	steps := []struct {
		gvr  dynamic.ResourceInterface
		name string
	}{
		{c.dynamic.Resource(nodeProviderGVR), cfg.NodeProviderName},
		{c.dynamic.Resource(argoAppGVR).Namespace(cfg.VCINamespace), cfg.ArgoApplicationName},
		{c.dynamic.Resource(clusterGVR), cfg.ConnectedClusterName},
	}
	for _, step := range steps {
		existing, err := step.gvr.Get(ctx, step.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !ownedBy(existing, obj) {
			return ownershipError("managed resource", existing, obj)
		}
		if existing.GetDeletionTimestamp() == nil {
			if err := step.gvr.Delete(ctx, step.name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		// Preserve ordering: NodeProvider -> agent application -> Cluster.
		return nil
	}
	if cfg.DeleteVCI {
		vcis := c.dynamic.Resource(vciGVR).Namespace(cfg.VCINamespace)
		existing, err := vcis.Get(ctx, cfg.VCIName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			if !ownedBy(existing, obj) {
				return ownershipError("VirtualClusterInstance", existing, obj)
			}
			if existing.GetDeletionTimestamp() == nil {
				if err := vcis.Delete(ctx, cfg.VCIName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			}
			return nil
		}
	}
	copy := obj.DeepCopy()
	copy.SetFinalizers(remove(copy.GetFinalizers(), finalizer))
	_, err := c.dynamic.Resource(providerClusterGVR).Namespace(obj.GetNamespace()).Update(ctx, copy, metav1.UpdateOptions{})
	return err
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
func remove(items []string, value string) []string {
	out := items[:0]
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}
