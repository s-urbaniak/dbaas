package provisioner

import (
	"testing"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func TestExternalBaseURLForHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		host   string
		scheme string
		port   string
		want   string
	}{
		{
			name:   "localhost with port",
			host:   "localhost:8090",
			scheme: "https",
			port:   "6443",
			want:   "https://localhost:6443",
		},
		{
			name:   "ipv4 with port",
			host:   "192.168.178.201:8090",
			scheme: "https",
			port:   "6443",
			want:   "https://192.168.178.201:6443",
		},
		{
			name:   "ipv6 with port",
			host:   "[::1]:8090",
			scheme: "http",
			port:   "4466",
			want:   "http://[::1]:4466",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ExternalBaseURLForHost(tt.host, tt.scheme, tt.port)
			if err != nil {
				t.Fatalf("ExternalBaseURLForHost() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ExternalBaseURLForHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExternalURLForServer(t *testing.T) {
	t.Parallel()

	got, err := externalURLForServer(
		"https://localhost:6443/clusters/root:consumers:test",
		"https://192.168.178.201:6443",
	)
	if err != nil {
		t.Fatalf("externalURLForServer() error = %v", err)
	}

	want := "https://192.168.178.201:6443/clusters/root:consumers:test"
	if got != want {
		t.Fatalf("externalURLForServer() = %q, want %q", got, want)
	}
}

func TestKubeconfigBytesForExternalBaseURLRejectsBadWorkspaceURL(t *testing.T) {
	t.Parallel()

	prov := &Provisioner{}
	workspace := &kcptenancyv1alpha1.Workspace{}
	workspace.Spec.URL = "://not-valid"

	if _, err := prov.KubeconfigBytesForExternalBaseURL(t.Context(), workspace, "https://localhost:6443"); err == nil {
		t.Fatal("expected error for invalid workspace URL")
	}
}
