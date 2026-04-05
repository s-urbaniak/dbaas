package main

import (
	"net/http/httptest"
	"testing"
)

func TestRequestExternalBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		target  string
		headers map[string]string
		scheme  string
		port    string
		want    string
	}{
		{
			name:   "localhost request host",
			target: "http://localhost:8090/",
			scheme: "https",
			port:   "6443",
			want:   "https://localhost:6443",
		},
		{
			name:   "ipv4 request host",
			target: "http://192.168.178.201:8090/",
			scheme: "https",
			port:   "6443",
			want:   "https://192.168.178.201:6443",
		},
		{
			name:   "loopback request host",
			target: "http://127.0.0.1:8090/",
			scheme: "http",
			port:   "4466",
			want:   "http://127.0.0.1:4466",
		},
		{
			name:   "forwarded header wins",
			target: "http://localhost:8090/",
			headers: map[string]string{
				"Forwarded": `for=192.0.2.10;host=192.168.178.201:8090;proto=http`,
			},
			scheme: "https",
			port:   "6443",
			want:   "https://192.168.178.201:6443",
		},
		{
			name:   "x-forwarded-host fallback",
			target: "http://localhost:8090/",
			headers: map[string]string{
				"X-Forwarded-Host": "127.0.0.1:8090, proxy.example",
			},
			scheme: "http",
			port:   "4466",
			want:   "http://127.0.0.1:4466",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", tt.target, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got, err := requestExternalBaseURL(req, tt.scheme, tt.port)
			if err != nil {
				t.Fatalf("requestExternalBaseURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("requestExternalBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
