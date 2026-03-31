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

// Package provisioner creates and manages KCP consumer workspaces for the DBaaS demo.
package provisioner

import (
	"context"
	"fmt"
	"net/url"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

var (
	workspaceGVR = schema.GroupVersionResource{
		Group:    "tenancy.kcp.io",
		Version:  "v1alpha1",
		Resource: "workspaces",
	}
	apiBindingGVR = schema.GroupVersionResource{
		Group:    "apis.kcp.io",
		Version:  "v1alpha1",
		Resource: "apibindings",
	}
)

// Provisioner creates and manages KCP consumer workspaces.
type Provisioner struct {
	// AdminConfig is the KCP admin REST config. Its Host may include a /clusters/... path.
	AdminConfig *rest.Config
	// ProviderWorkspace is the KCP path of the service-provider workspace (e.g. "root:dbaas-provider").
	ProviderWorkspace string
	// ExportName is the name of the APIExport to bind (e.g. "mongodatabases.dbaas.mongodb.com").
	ExportName string
	// ConsumersWorkspace is the KCP path of the consumer org workspace (e.g. "root:consumers").
	ConsumersWorkspace string
}

// WorkspaceInfo holds display information about a consumer workspace.
type WorkspaceInfo struct {
	Name  string
	Phase string
	URL   string
}

// kcpBaseURL strips the /clusters/... path from a KCP server URL, returning just the host+port.
func kcpBaseURL(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("parsing KCP server URL %q: %w", server, err)
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// configForWorkspace returns a REST config scoped to a specific KCP workspace path.
func (p *Provisioner) configForWorkspace(wsPath string) (*rest.Config, error) {
	base, err := kcpBaseURL(p.AdminConfig.Host)
	if err != nil {
		return nil, err
	}
	cfg := rest.CopyConfig(p.AdminConfig)
	cfg.Host = base + "/clusters/" + wsPath
	return cfg, nil
}

// ProvisionWorkspace creates a new consumer workspace under ConsumersWorkspace and binds
// the DBaaS APIExport into it. Returns the workspace URL from status once ready.
func (p *Provisioner) ProvisionWorkspace(ctx context.Context, name string) (string, error) {
	consumersCfg, err := p.configForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return "", err
	}
	consumersClient, err := dynamic.NewForConfig(consumersCfg)
	if err != nil {
		return "", fmt.Errorf("creating consumers dynamic client: %w", err)
	}

	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"type": map[string]any{"name": "universal"},
		},
	}}
	if _, err := consumersClient.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating workspace %q: %w", name, err)
	}

	// Poll until the workspace is Ready and has a URL.
	var wsURL string
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, false,
		func(ctx context.Context) (bool, error) {
			obj, err := consumersClient.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil // not found yet — keep polling
			}
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			u, _, _ := unstructured.NestedString(obj.Object, "spec", "URL")
			if phase == "Ready" && u != "" {
				wsURL = u
				return true, nil
			}
			return false, nil
		},
	); err != nil {
		return "", fmt.Errorf("waiting for workspace %q to be ready: %w", name, err)
	}

	// Create APIBinding in the new workspace.
	wsPath := p.ConsumersWorkspace + ":" + name
	wsCfg, err := p.configForWorkspace(wsPath)
	if err != nil {
		return "", err
	}
	wsClient, err := dynamic.NewForConfig(wsCfg)
	if err != nil {
		return "", fmt.Errorf("creating workspace dynamic client: %w", err)
	}

	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "dbaas"},
		"spec": map[string]any{
			"reference": map[string]any{
				"export": map[string]any{
					"name": p.ExportName,
					"path": p.ProviderWorkspace,
				},
			},
		},
	}}
	if _, err := wsClient.Resource(apiBindingGVR).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating APIBinding in workspace %q: %w", name, err)
	}

	return wsURL, nil
}

// GetWorkspaceURL returns the URL of an existing consumer workspace.
func (p *Provisioner) GetWorkspaceURL(ctx context.Context, name string) (string, error) {
	consumersCfg, err := p.configForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return "", err
	}
	consumersClient, err := dynamic.NewForConfig(consumersCfg)
	if err != nil {
		return "", err
	}
	obj, err := consumersClient.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting workspace %q: %w", name, err)
	}
	u, _, _ := unstructured.NestedString(obj.Object, "spec", "URL")
	if u == "" {
		return "", fmt.Errorf("workspace %q has no URL (not ready yet?)", name)
	}
	return u, nil
}

// ListWorkspaces returns all consumer workspaces under ConsumersWorkspace.
func (p *Provisioner) ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	consumersCfg, err := p.configForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return nil, err
	}
	consumersClient, err := dynamic.NewForConfig(consumersCfg)
	if err != nil {
		return nil, err
	}
	list, err := consumersClient.Resource(workspaceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	result := make([]WorkspaceInfo, 0, len(list.Items))
	for _, item := range list.Items {
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		u, _, _ := unstructured.NestedString(item.Object, "spec", "URL")
		result = append(result, WorkspaceInfo{Name: item.GetName(), Phase: phase, URL: u})
	}
	return result, nil
}

// KubeconfigBytes generates a kubeconfig YAML for the given workspace URL,
// copying TLS and auth credentials from the admin config.
//
// NOTE (dev only): this reuses the admin token. In production, issue a
// per-workspace ServiceAccount token or use OIDC.
func (p *Provisioner) KubeconfigBytes(wsURL string) ([]byte, error) {
	cluster := clientcmdapi.NewCluster()
	cluster.Server = wsURL
	cluster.InsecureSkipTLSVerify = p.AdminConfig.Insecure
	if !cluster.InsecureSkipTLSVerify {
		cluster.CertificateAuthorityData = p.AdminConfig.CAData
	}

	authInfo := clientcmdapi.NewAuthInfo()
	switch {
	case p.AdminConfig.BearerToken != "":
		authInfo.Token = p.AdminConfig.BearerToken
	case len(p.AdminConfig.CertData) > 0:
		authInfo.ClientCertificateData = p.AdminConfig.CertData
		authInfo.ClientKeyData = p.AdminConfig.KeyData
	default:
		authInfo.ClientCertificate = p.AdminConfig.CertFile
		authInfo.ClientKey = p.AdminConfig.KeyFile
	}

	kubeCtx := clientcmdapi.NewContext()
	kubeCtx.Cluster = "kcp"
	kubeCtx.AuthInfo = "admin"

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kcp"] = cluster
	cfg.AuthInfos["admin"] = authInfo
	cfg.Contexts["workspace"] = kubeCtx
	cfg.CurrentContext = "workspace"

	return clientcmd.Write(*cfg)
}
