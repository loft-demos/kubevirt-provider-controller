package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestPlatformSubresources(t *testing.T) {
	requests := make(chan string, 2)
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests <- r.Method + " " + r.URL.Path
		var response string
		status := http.StatusOK
		switch r.URL.Path {
		case "/kubernetes/management/apis/management.loft.sh/v1/clusters/provider-a/accesskey":
			response = `{"accessKey":"secret","loftHost":"https://platform.example.com"}`
		case "/kubernetes/management/apis/management.loft.sh/v1/namespaces/p-default/virtualclusterinstances/provider-a/kubeconfig":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			spec := body["spec"].(map[string]interface{})
			if spec["clientCert"] != true {
				t.Errorf("clientCert was not requested")
			}
			response = `{"status":{"kubeConfig":"apiVersion: v1"}}`
		default:
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	p, err := newPlatformClient(&rest.Config{Host: "https://platform.test/kubernetes/management", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	access, err := p.clusterAccessKey(context.Background(), "provider-a")
	if err != nil || access.AccessKey != "secret" {
		t.Fatalf("access key: %#v, %v", access, err)
	}
	kcfg, err := p.virtualClusterKubeconfig(context.Background(), "p-default", "provider-a", 600)
	if err != nil || string(kcfg) != "apiVersion: v1" {
		t.Fatalf("kubeconfig: %q, %v", kcfg, err)
	}
	close(requests)
	want := []string{
		"GET /kubernetes/management/apis/management.loft.sh/v1/clusters/provider-a/accesskey",
		"POST /kubernetes/management/apis/management.loft.sh/v1/namespaces/p-default/virtualclusterinstances/provider-a/kubeconfig",
	}
	i := 0
	for got := range requests {
		if got != want[i] {
			t.Errorf("request %d: got %q, want %q", i, got, want[i])
		}
		i++
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
