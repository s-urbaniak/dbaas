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
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/s-urbaniak/dbaas/internal/provisioner"
)

//go:embed static/index.html
var indexHTML string

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

type pageData struct {
	Workspaces []provisioner.WorkspaceInfo
	Error      string
	Success    string
}

func main() {
	var (
		addr               string
		kubeconfigPath     string
		providerWorkspace  string
		exportName         string
		consumersWorkspace string
	)
	flag.StringVar(&addr, "addr", ":8090", "HTTP listen address.")
	flag.StringVar(&kubeconfigPath, "kubeconfig", os.Getenv("KUBECONFIG"), "Path to KCP admin kubeconfig.")
	flag.StringVar(&providerWorkspace, "provider-workspace", "root:dbaas-provider", "KCP path of the service-provider workspace.")
	flag.StringVar(&exportName, "export-name", "mongodatabases.dbaas.mongodb.com", "APIExport name to bind in consumer workspaces.")
	flag.StringVar(&consumersWorkspace, "consumers-workspace", "root:consumers", "KCP path of the consumer org workspace.")
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
		AdminConfig:        cfg,
		ProviderWorkspace:  providerWorkspace,
		ExportName:         exportName,
		ConsumersWorkspace: consumersWorkspace,
		K8sClient:          k8sClient,
		HeadlampNamespace:  "headlamp",
		HeadlampSecret:     "headlamp-workspace-kubeconfig",
		HeadlampDeployment: "headlamp",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex(prov))
	mux.HandleFunc("POST /api/workspaces", handleCreateWorkspace(prov))
	mux.HandleFunc("GET /api/workspaces/{name}/kubeconfig", handleKubeconfig(prov))
	mux.HandleFunc("POST /api/workspaces/{name}/delete", handleDeleteWorkspace(prov))

	go startHeadlampReconcileLoop(prov)

	slog.Info("starting provisioner", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

func startHeadlampReconcileLoop(prov *provisioner.Provisioner) {
	prov.ReconcileHeadlamp(context.Background())

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		prov.ReconcileHeadlamp(context.Background())
	}
}

func handleIndex(prov *provisioner.Provisioner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := pageData{}
		if msg := r.URL.Query().Get("success"); msg != "" {
			data.Success = msg
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			data.Error = errMsg
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
		if _, err := prov.ProvisionWorkspace(r.Context(), name); err != nil {
			http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?success="+url.QueryEscape("Workspace "+name+" provisioned successfully"), http.StatusSeeOther)
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
		wsURL, err := prov.GetWorkspaceURL(r.Context(), name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		data, err := prov.KubeconfigBytes(wsURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.kubeconfig"`, name))
		_, _ = w.Write(data)
	}
}
