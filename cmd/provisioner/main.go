/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/s-urbaniak/dbaas/internal/provisioner"
)

//go:embed static/index.html
var indexHTML string

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

type pageData struct {
	Workspaces      []provisioner.WorkspaceInfo
	Error           string
	Success         string
	HeadlampBaseURL string
}

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	var (
		addr               string
		kubeconfigPath     string
		providerWorkspace  string
		exportName         string
		kubernetesExport   string
		consumersWorkspace string
	)
	flag.StringVar(&addr, "addr", ":8090", "HTTP listen address.")
	flag.StringVar(&kubeconfigPath, "kubeconfig", os.Getenv("KUBECONFIG"), "Path to kcp admin kubeconfig.")
	flag.StringVar(&providerWorkspace, "provider-workspace", "root:dbaas-provider", "kcp path of the service-provider workspace.")
	flag.StringVar(&exportName, "export-name", "databases.dbaas.mongodb.com", "Database APIExport name to bind in consumer workspaces.")
	flag.StringVar(&kubernetesExport, "kubernetes-export-name", "kubernetes.dbaas.mongodb.com", "Kubernetes APIExport name to bind in consumer workspaces.")
	flag.StringVar(&consumersWorkspace, "consumers-workspace", "root:consumers", "kcp path of the consumer org workspace.")
	flag.Parse()

	if kubeconfigPath == "" {
		slog.Error("--kubeconfig or KUBECONFIG env var is required")
		os.Exit(1)
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		slog.Error("loading kubeconfig", "err", err)
		os.Exit(1)
	}

	var k8sClient kubernetes.Interface
	if k8sCfg, err := rest.InClusterConfig(); err == nil {
		if c, err := kubernetes.NewForConfig(k8sCfg); err == nil {
			k8sClient = c
		} else {
			slog.Warn("headlamp integration disabled: building k8s client failed", "err", err)
		}
	} else {
		slog.Info("headlamp integration disabled: not running in-cluster")
	}

	prov := &provisioner.Provisioner{
		ProcessContext:    ctx,
		AdminConfig:       cfg,
		ProviderWorkspace: providerWorkspace,
		Bindings: []provisioner.WorkspaceBinding{
			{Name: "dbaas", ExportName: exportName, PermissionClaims: []kcpapisv1alpha2.AcceptablePermissionClaim{acceptedEventsClaim()}},
			{Name: "kubernetes", ExportName: kubernetesExport, PermissionClaims: []kcpapisv1alpha2.AcceptablePermissionClaim{acceptedEventsClaim()}},
		},
		ConsumersWorkspace: consumersWorkspace,
		K8sClient:          k8sClient,
		HeadlampNamespace:  "headlamp",
		HeadlampSecret:     "headlamp-workspace-kubeconfig",
		HeadlampDeployment: "headlamp",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex(prov))
	mux.HandleFunc("GET /api/admin.kubeconfig", handleAdminKubeconfig(prov))
	mux.HandleFunc("POST /api/workspaces", handleCreateWorkspace(prov))
	mux.HandleFunc("GET /api/workspaces/{name}/kubeconfig", handleKubeconfig(prov))
	mux.HandleFunc("POST /api/workspaces/{name}/delete", handleDeleteWorkspace(prov))

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go startHeadlampReconcileLoop(ctx, prov)
	go shutdownServerOnSignal(ctx, server)

	slog.Info("starting provisioner", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

func startHeadlampReconcileLoop(ctx context.Context, prov *provisioner.Provisioner) {
	prov.ReconcileWorkspaceBindings(ctx)
	prov.ReconcileHeadlamp(ctx)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prov.ReconcileWorkspaceBindings(ctx)
			prov.ReconcileHeadlamp(ctx)
		}
	}
}

func shutdownServerOnSignal(ctx context.Context, server *http.Server) {
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "err", err)
	}
}

func handleIndex(prov *provisioner.Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := pageData{
			HeadlampBaseURL: "http://localhost:4466",
		}
		if msg := r.URL.Query().Get("success"); msg != "" {
			data.Success = msg
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			data.Error = errMsg
		}
		if baseURL, err := requestExternalBaseURL(r, "http", "4466"); err == nil {
			data.HeadlampBaseURL = baseURL
		}
		workspaces, err := prov.ListWorkspaces(r.Context())
		if err != nil {
			data.Error = fmt.Sprintf("listing workspaces: %v", err)
		} else {
			data.Workspaces = workspaces
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(w, data); err != nil {
			slog.Error("rendering template", "err", err)
		}
	}
}

func handleCreateWorkspace(prov *provisioner.Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.FormValue("name")
		if name == "" {
			http.Redirect(w, r, "/?error="+url.QueryEscape("workspace name is required"), http.StatusSeeOther)
			return
		}
		if err := prov.ProvisionWorkspace(r.Context(), name); err != nil {
			http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?success="+url.QueryEscape("Workspace "+name+" provisioned successfully"), http.StatusSeeOther)
	}
}

func handleAdminKubeconfig(prov *provisioner.Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		baseURL, err := requestExternalBaseURL(r, "https", "6443")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, err := prov.AdminKubeconfigBytesForExternalBaseURL(r.Context(), baseURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Header().Set("Content-Disposition", `attachment; filename="admin.kubeconfig"`)
		_, _ = w.Write(data)
	}
}

func handleDeleteWorkspace(prov *provisioner.Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := prov.DeleteWorkspace(r.Context(), name); err != nil {
			http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?success="+url.QueryEscape("Workspace "+name+" deleted"), http.StatusSeeOther)
	}
}

func handleKubeconfig(prov *provisioner.Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		workspace, err := prov.GetWorkspace(r.Context(), name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if workspace.Spec.URL == "" {
			http.Error(w, fmt.Sprintf("workspace %q has no URL (not ready yet?)", name), http.StatusConflict)
			return
		}
		baseURL, err := requestExternalBaseURL(r, "https", "6443")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, err := prov.KubeconfigBytesForExternalBaseURL(r.Context(), workspace, baseURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.kubeconfig"`, name))
		_, _ = w.Write(data)
	}
}

func requestExternalBaseURL(r *http.Request, scheme, port string) (string, error) {
	host := forwardedOrRequestHost(r)
	return provisioner.ExternalBaseURLForHost(host, scheme, port)
}

func forwardedOrRequestHost(r *http.Request) string {
	if host := forwardedHeaderHost(r.Header.Get("Forwarded")); host != "" {
		return host
	}
	if host := firstForwardedHost(r.Header.Get("X-Forwarded-Host")); host != "" {
		return host
	}
	return r.Host
}

func firstForwardedHost(value string) string {
	if value == "" {
		return ""
	}
	first, _, _ := strings.Cut(value, ",")
	return strings.TrimSpace(first)
}

func forwardedHeaderHost(value string) string {
	for _, entry := range strings.Split(value, ",") {
		for _, part := range strings.Split(entry, ";") {
			part = strings.TrimSpace(part)
			if len(part) < 5 || !strings.EqualFold(part[:5], "host=") {
				continue
			}
			host := strings.TrimSpace(part[5:])
			return strings.Trim(host, `"`)
		}
	}
	return ""
}

// acceptedEventsClaim returns a permission claim that accepts all events with full access.
func acceptedEventsClaim() kcpapisv1alpha2.AcceptablePermissionClaim {
	return kcpapisv1alpha2.AcceptablePermissionClaim{
		ScopedPermissionClaim: kcpapisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: kcpapisv1alpha2.PermissionClaim{
				GroupResource: kcpapisv1alpha2.GroupResource{Resource: "events"},
				Verbs:         []string{"*"},
			},
			Selector: kcpapisv1alpha2.PermissionClaimSelector{MatchAll: true},
		},
		State: kcpapisv1alpha2.ClaimAccepted,
	}
}
