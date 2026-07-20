package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type platformClient struct {
	config *rest.Config
	http   *http.Client
}

type agentAccess struct {
	AccessKey string `json:"accessKey"`
	LoftHost  string `json:"loftHost"`
	CACert    string `json:"caCert"`
	Insecure  bool   `json:"insecure"`
}

func newPlatformClient(cfg *rest.Config) (*platformClient, error) {
	h, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	return &platformClient{config: cfg, http: h}, nil
}

func (p *platformClient) endpoint(parts ...string) (string, error) {
	u, err := url.Parse(p.config.Host)
	if err != nil {
		return "", err
	}
	all := append([]string{u.Path}, parts...)
	u.Path = path.Join(all...)
	return u.String(), nil
}

func (p *platformClient) do(ctx context.Context, method string, endpointParts []string, body interface{}, out interface{}) error {
	endpoint, err := p.endpoint(endpointParts...)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Platform API %s %s returned %s: %s", method, endpoint, resp.Status, string(b))
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (p *platformClient) clusterAccessKey(ctx context.Context, name string) (agentAccess, error) {
	var result agentAccess
	err := p.do(ctx, http.MethodGet, []string{"apis", "management.loft.sh", "v1", "clusters", name, "accesskey"}, nil, &result)
	if err == nil && (result.AccessKey == "" || result.LoftHost == "") {
		err = fmt.Errorf("cluster access-key response omitted accessKey or loftHost")
	}
	return result, err
}

func (p *platformClient) virtualClusterKubeconfig(ctx context.Context, namespace, name string, ttl int64) ([]byte, error) {
	req := map[string]interface{}{
		"apiVersion": "management.loft.sh/v1", "kind": "VirtualClusterInstanceKubeConfig",
		"metadata": map[string]interface{}{"namespace": namespace},
		// A token kubeconfig can use the Platform proxy, which is the only API
		// endpoint guaranteed to exist for a Private Node vCluster. Platform uses
		// certificateTTL as the generated access key TTL for this request too.
		"spec": map[string]interface{}{"certificateTTL": ttl, "clientCert": false},
	}
	var result struct {
		Status struct {
			KubeConfig string `json:"kubeConfig"`
		} `json:"status"`
	}
	err := p.do(ctx, http.MethodPost, []string{"apis", "management.loft.sh", "v1", "namespaces", namespace, "virtualclusterinstances", name, "kubeconfig"}, req, &result)
	if err != nil {
		return nil, err
	}
	if result.Status.KubeConfig == "" {
		return nil, fmt.Errorf("virtual cluster kubeconfig response was empty")
	}
	return []byte(result.Status.KubeConfig), nil
}

func stageAgentSecret(ctx context.Context, kubeconfig []byte, namespace, name string, access agentAccess, ownerLabels map[string]string) error {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("parse temporary kubeconfig: %w", err)
	}
	cfg.UserAgent = "kubevirt-provider-controller-bootstrap"
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("create temporary virtual-cluster client: %w", err)
	}
	if _, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: ownerLabels}}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create agent namespace: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get agent namespace: %w", err)
	}

	secrets := client.CoreV1().Secrets(namespace)
	desired := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: ownerLabels}, Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{"token": access.AccessKey, "url": access.LoftHost}}
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.StringData = desired.StringData
	_, err = secrets.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}
