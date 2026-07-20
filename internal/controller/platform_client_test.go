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
			if got := r.Header.Get("Impersonate-User"); got != "admin" {
				t.Errorf("Impersonate-User = %q, want admin", got)
			}
			if got := r.Header.Values("Impersonate-Group"); len(got) != 1 || got[0] != "loft:user:admin" {
				t.Errorf("Impersonate-Group = %#v, want loft:user:admin", got)
			}
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			spec := body["spec"].(map[string]interface{})
			if spec["clientCert"] != false {
				t.Errorf("clientCert must be false so the kubeconfig uses the Platform proxy")
			}
			if spec["certificateTTL"] != float64(600) {
				t.Errorf("certificateTTL = %#v, want 600", spec["certificateTTL"])
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
	kcfg, err := p.virtualClusterKubeconfig(context.Background(), "p-default", "provider-a", 600, "admin", "")
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

func TestPlatformOwnerImpersonation(t *testing.T) {
	tests := []struct {
		name, user, team, wantUser, wantGroup string
	}{
		{name: "user", user: "admin", wantUser: "admin", wantGroup: "loft:user:admin"},
		{name: "team", team: "platform-admins", wantUser: "loft:team:platform-admins", wantGroup: "loft:team:platform-admins"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotGroup := platformOwnerImpersonation(tt.user, tt.team)
			if gotUser != tt.wantUser || gotGroup != tt.wantGroup {
				t.Fatalf("got user=%q group=%q, want user=%q group=%q", gotUser, gotGroup, tt.wantUser, tt.wantGroup)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
