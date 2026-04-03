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
	"log/slog"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// IsTransientPhase reports whether the given phase string represents a workspace
// that is not yet in a stable end-state (Ready or fully deleted).
func IsTransientPhase(phase string) bool {
	switch phase {
	case "Ready", "":
		return false
	}
	return true
}

var (
	workspaceGVR = schema.GroupVersionResource{
		Group:    "tenancy.kcp.io",
		Version:  "v1alpha1",
		Resource: "workspaces",
	}
	logicalClusterGVR = schema.GroupVersionResource{
		Group:    "core.kcp.io",
		Version:  "v1alpha1",
		Resource: "logicalclusters",
	}
	apiBindingGVR = schema.GroupVersionResource{
		Group:    "apis.kcp.io",
		Version:  "v1alpha1",
		Resource: "apibindings",
	}
	mongodbDatabaseGVR = schema.GroupVersionResource{
		Group:    "kro.run",
		Version:  "v1alpha1",
		Resource: "mongodbdatabases",
	}
)

// Provisioner creates and manages KCP consumer workspaces.
type Provisioner struct {
	// ProcessContext is canceled when the provisioner process is shutting down.
	ProcessContext context.Context
	// AdminConfig is the KCP admin REST config. Its Host may include a /clusters/... path.
	AdminConfig *rest.Config
	// ProviderWorkspace is the KCP path of the service-provider workspace (e.g. "root:dbaas-provider").
	ProviderWorkspace string
	// ExportName is the name of the APIExport to bind (e.g. "mongodatabases.dbaas.mongodb.com").
	ExportName string
	// ConsumersWorkspace is the KCP path of the consumer org workspace (e.g. "root:consumers").
	ConsumersWorkspace string

	// Headlamp integration (optional — no-op when K8sClient is nil).
	// K8sClient is the in-cluster Kubernetes client used to update the Headlamp kubeconfig.
	K8sClient kubernetes.Interface
	// HeadlampNamespace is the namespace where Headlamp is deployed (e.g. "headlamp").
	HeadlampNamespace string
	// HeadlampSecret is the name of the Secret holding Headlamp's workspace kubeconfig.
	HeadlampSecret string
	// HeadlampDeployment is the name of the Headlamp Deployment to rolling-restart after updates.
	HeadlampDeployment string
}

// WorkspaceInfo holds display information about a consumer workspace.
type WorkspaceInfo struct {
	Name          string
	Phase         string
	Status        string
	StatusClass   string
	StatusDetail  string
	Transient     bool
	URL           string
	DatabaseCount int
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

	if err := p.ensureWorkspaceBinding(ctx, wsClient); err != nil {
		return "", fmt.Errorf("creating APIBinding in workspace %q: %w", name, err)
	}

	go p.syncHeadlamp(p.ProcessContext, name, true)
	return wsURL, nil
}

func (p *Provisioner) desiredWorkspaceBinding() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
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
}

func (p *Provisioner) ensureWorkspaceBinding(
	ctx context.Context,
	wsClient dynamic.Interface,
) error {
	if _, err := wsClient.Resource(apiBindingGVR).Get(ctx, "dbaas", metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	_, err := wsClient.Resource(apiBindingGVR).Create(
		ctx,
		p.desiredWorkspaceBinding(),
		metav1.CreateOptions{},
	)
	return err
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
		info := WorkspaceInfo{
			Name:        item.GetName(),
			Phase:       phase,
			Status:      phase,
			StatusClass: "warning text-dark",
			URL:         u,
		}

		if item.GetDeletionTimestamp() != nil {
			phase = "Terminating"
			info.Phase = phase
			info.Status = "Terminating"
			info.StatusClass = "secondary"
			info.Transient = true
			info.Status, info.StatusDetail = p.logicalClusterDeletionStatus(ctx, item)
			if info.Status == "Finalizing parent" {
				info.StatusClass = "info"
			} else if info.Status == "Deleting content" {
				info.StatusClass = "secondary"
			}
			result = append(result, info)
			continue
		}

		if phase == "Ready" {
			info.Status = "Ready"
			info.StatusClass = "success"
			info.DatabaseCount = p.countDatabases(ctx, p.ConsumersWorkspace+":"+item.GetName())
		} else if phase != "" {
			info.Transient = IsTransientPhase(phase)
		} else {
			info.Status = "—"
		}

		result = append(result, info)
	}
	return result, nil
}

func (p *Provisioner) logicalClusterDeletionStatus(
	ctx context.Context,
	workspace unstructured.Unstructured,
) (string, string) {
	const (
		logicalClusterObjectName    = "cluster"
		workspaceContentDeletedType = "WorkspaceContentDeleted"
	)

	clusterName, _, _ := unstructured.NestedString(workspace.Object, "spec", "cluster")
	if clusterName == "" {
		return "Finalizing parent", "Waiting for workspace cleanup after scheduling metadata removal."
	}

	cfg, err := p.configForWorkspace(clusterName)
	if err != nil {
		return "Terminating", ""
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "Terminating", ""
	}

	logicalCluster, err := client.Resource(logicalClusterGVR).Get(
		ctx,
		logicalClusterObjectName,
		metav1.GetOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "Finalizing parent", "LogicalCluster is gone; waiting for the parent Workspace object to be removed."
		}
		return "Terminating", ""
	}

	conditions, _, _ := unstructured.NestedSlice(logicalCluster.Object, "status", "conditions")
	for _, rawCondition := range conditions {
		condition, ok := rawCondition.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] != workspaceContentDeletedType {
			continue
		}

		message, _ := condition["message"].(string)
		status, _ := condition["status"].(string)
		if status == "True" {
			return "Finalizing parent", "Workspace content is deleted; waiting for the parent Workspace object to be removed."
		}
		if message != "" {
			return "Deleting content", message
		}
		return "Deleting content", ""
	}

	if !logicalCluster.GetDeletionTimestamp().IsZero() {
		return "Deleting content", "LogicalCluster deletion is in progress."
	}

	return "Terminating", ""
}

// countDatabases returns the number of MongoDBDatabase resources in the given workspace.
// Returns 0 on any error (e.g. workspace not yet fully initialised).
func (p *Provisioner) countDatabases(ctx context.Context, wsPath string) int {
	cfg, err := p.configForWorkspace(wsPath)
	if err != nil {
		return 0
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return 0
	}
	list, err := client.Resource(mongodbDatabaseGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0
	}
	return len(list.Items)
}

// DeleteWorkspace deletes a consumer workspace under ConsumersWorkspace and
// waits until the workspace is fully removed from the API.
func (p *Provisioner) DeleteWorkspace(ctx context.Context, name string) error {
	consumersCfg, err := p.configForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return err
	}
	consumersClient, err := dynamic.NewForConfig(consumersCfg)
	if err != nil {
		return fmt.Errorf("creating consumers dynamic client: %w", err)
	}
	if err := consumersClient.Resource(workspaceGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("deleting workspace %q: %w", name, err)
	}
	go p.syncHeadlamp(p.ProcessContext, name, false)
	return nil
}

// ReconcileHeadlamp refreshes the Headlamp workspace kubeconfig from the current
// consumer workspace set. It is safe to call periodically.
func (p *Provisioner) ReconcileHeadlamp(ctx context.Context) {
	p.syncHeadlamp(ctx, "", false)
}

// ReconcileWorkspaceBindings ensures all non-terminating consumer workspaces have
// the expected DBaaS APIBinding.
func (p *Provisioner) ReconcileWorkspaceBindings(ctx context.Context) {
	workspaces, err := p.ListWorkspaces(ctx)
	if err != nil {
		slog.Error("workspace binding reconcile: list workspaces", "err", err)
		return
	}

	for _, workspace := range workspaces {
		if workspace.Transient {
			continue
		}

		cfg, err := p.configForWorkspace(p.ConsumersWorkspace + ":" + workspace.Name)
		if err != nil {
			slog.Error("workspace binding reconcile: config workspace",
				"workspace", workspace.Name, "err", err)
			continue
		}
		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			slog.Error("workspace binding reconcile: client workspace",
				"workspace", workspace.Name, "err", err)
			continue
		}
		if err := p.ensureWorkspaceBinding(ctx, client); err != nil {
			slog.Error("workspace binding reconcile: ensure binding",
				"workspace", workspace.Name, "err", err)
		}
	}
}

// syncHeadlamp adds (add=true) or removes (add=false) a workspace context from
// the Headlamp kubeconfig Secret, then triggers a rolling restart of the Headlamp
// Deployment so the change is picked up. Errors are logged but never propagate to
// the caller — Headlamp sync is best-effort and must not block workspace operations.
func (p *Provisioner) syncHeadlamp(ctx context.Context, wsName string, add bool) {
	if p.K8sClient == nil {
		return
	}

	secret, err := p.K8sClient.CoreV1().Secrets(p.HeadlampNamespace).Get(ctx, p.HeadlampSecret, metav1.GetOptions{})
	if err != nil {
		slog.Error("headlamp sync: get secret", "err", err)
		return
	}

	var cfg *clientcmdapi.Config
	if raw := secret.Data["config"]; len(raw) > 0 {
		cfg, err = clientcmd.Load(raw)
		if err != nil {
			cfg = clientcmdapi.NewConfig()
		}
	} else {
		cfg = clientcmdapi.NewConfig()
	}

	desiredWorkspaces, err := p.listHeadlampWorkspaceNames(ctx)
	if err != nil {
		slog.Error("headlamp sync: list consumer workspaces", "err", err)
		return
	}

	if add {
		desiredWorkspaces[wsName] = struct{}{}
	} else {
		delete(desiredWorkspaces, wsName)
	}

	p.removeStaleHeadlampWorkspaces(cfg, desiredWorkspaces)

	for workspaceName := range desiredWorkspaces {
		cluster := clientcmdapi.NewCluster()
		cluster.Server = fmt.Sprintf(
			"https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/%s:%s",
			p.ConsumersWorkspace, workspaceName,
		)
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
		kubeCtx.Cluster = workspaceName
		kubeCtx.AuthInfo = workspaceName

		cfg.Clusters[workspaceName] = cluster
		cfg.AuthInfos[workspaceName] = authInfo
		cfg.Contexts[workspaceName] = kubeCtx
	}

	data, err := clientcmd.Write(*cfg)
	if err != nil {
		slog.Error("headlamp sync: marshal kubeconfig", "err", err)
		return
	}

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data["config"] = data
	if _, err := p.K8sClient.CoreV1().Secrets(p.HeadlampNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		slog.Error("headlamp sync: update secret", "err", err)
		return
	}

	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))
	if _, err := p.K8sClient.AppsV1().Deployments(p.HeadlampNamespace).Patch(
		ctx, p.HeadlampDeployment, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	); err != nil {
		slog.Error("headlamp sync: restart deployment", "err", err)
	}
}

func (p *Provisioner) listHeadlampWorkspaceNames(
	ctx context.Context,
) (map[string]struct{}, error) {
	workspaces, err := p.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}

	names := make(map[string]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		if workspace.Phase == "Terminating" {
			continue
		}
		names[workspace.Name] = struct{}{}
	}

	return names, nil
}

func (p *Provisioner) removeStaleHeadlampWorkspaces(
	cfg *clientcmdapi.Config,
	desiredWorkspaces map[string]struct{},
) {
	managedPrefix := fmt.Sprintf("/clusters/%s:", p.ConsumersWorkspace)

	for name, cluster := range cfg.Clusters {
		if !strings.Contains(cluster.Server, managedPrefix) {
			continue
		}
		if _, keep := desiredWorkspaces[name]; keep {
			continue
		}

		delete(cfg.Clusters, name)
		delete(cfg.AuthInfos, name)
		delete(cfg.Contexts, name)
	}
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
