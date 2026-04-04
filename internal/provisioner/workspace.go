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

	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	mongodbDatabaseGVR = schema.GroupVersionResource{
		Group:    "kro.run",
		Version:  "v1alpha1",
		Resource: "mongodbdatabases",
	}
)

const (
	workspaceCredentialAnnotation = "dbaas.mongodb.com/scoped-kubeconfig"
	workspaceServiceAccountName   = "workspace-admin"
	workspaceTokenSecretName      = "workspace-admin-token"
	workspaceClusterRoleBindName  = "workspace-service-account-admin"
	workspaceDefaultNamespace     = "default"
)

type workspaceCredentialMode string

const (
	workspaceCredentialsAdmin  workspaceCredentialMode = "admin"
	workspaceCredentialsScoped workspaceCredentialMode = "scoped"
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

func (p *Provisioner) kcpClientForWorkspace(wsPath string) (kcpclientset.Interface, error) {
	cfg, err := p.configForWorkspace(wsPath)
	if err != nil {
		return nil, err
	}
	return kcpclientset.NewForConfig(cfg)
}

func (p *Provisioner) kubeClientForWorkspace(wsPath string) (kubernetes.Interface, error) {
	cfg, err := p.configForWorkspace(wsPath)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// ProvisionWorkspace creates a new consumer workspace under ConsumersWorkspace and binds
// the DBaaS APIExport into it. Returns the workspace URL from status once ready.
func (p *Provisioner) ProvisionWorkspace(ctx context.Context, name string) (string, error) {
	consumersClient, err := p.kcpClientForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return "", fmt.Errorf("creating consumers client: %w", err)
	}

	ws := &kcptenancyv1alpha1.Workspace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "tenancy.kcp.io/v1alpha1",
			Kind:       "Workspace",
		},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kcptenancyv1alpha1.WorkspaceSpec{
			Type: &kcptenancyv1alpha1.WorkspaceTypeReference{
				Name: kcptenancyv1alpha1.WorkspaceTypeName("universal"),
			},
		},
	}
	if _, err := consumersClient.TenancyV1alpha1().Workspaces().Create(ctx, ws, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating workspace %q: %w", name, err)
	}

	// Poll until the workspace is Ready and has a URL.
	var wsURL string
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, false,
		func(ctx context.Context) (bool, error) {
			workspace, err := consumersClient.TenancyV1alpha1().Workspaces().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil // not found yet — keep polling
			}
			if string(workspace.Status.Phase) == "Ready" && workspace.Spec.URL != "" {
				wsURL = workspace.Spec.URL
				return true, nil
			}
			return false, nil
		},
	); err != nil {
		return "", fmt.Errorf("waiting for workspace %q to be ready: %w", name, err)
	}

	// Create APIBinding in the new workspace.
	wsPath := p.ConsumersWorkspace + ":" + name
	wsClient, err := p.kcpClientForWorkspace(wsPath)
	if err != nil {
		return "", fmt.Errorf("creating workspace client: %w", err)
	}

	if err := p.ensureWorkspaceBinding(ctx, wsClient); err != nil {
		return "", fmt.Errorf("creating APIBinding in workspace %q: %w", name, err)
	}

	if err := p.ensureScopedWorkspaceCredentials(ctx, wsPath); err != nil {
		return "", fmt.Errorf("creating scoped credentials in workspace %q: %w", name, err)
	}
	if err := p.markWorkspaceScopedCredentials(ctx, consumersClient, name); err != nil {
		return "", fmt.Errorf("marking workspace %q for scoped credentials: %w", name, err)
	}

	go p.syncHeadlamp(p.ProcessContext, name, true)
	return wsURL, nil
}

func (p *Provisioner) desiredWorkspaceBinding() *kcpapisv1alpha1.APIBinding {
	return &kcpapisv1alpha1.APIBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apis.kcp.io/v1alpha1",
			Kind:       "APIBinding",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "dbaas"},
		Spec: kcpapisv1alpha1.APIBindingSpec{
			Reference: kcpapisv1alpha1.BindingReference{
				Export: &kcpapisv1alpha1.ExportBindingReference{
					Name: p.ExportName,
					Path: p.ProviderWorkspace,
				},
			},
		},
	}
}

func (p *Provisioner) ensureWorkspaceBinding(
	ctx context.Context,
	wsClient kcpclientset.Interface,
) error {
	if _, err := wsClient.ApisV1alpha1().APIBindings().Get(ctx, "dbaas", metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	_, err := wsClient.ApisV1alpha1().APIBindings().Create(
		ctx,
		p.desiredWorkspaceBinding(),
		metav1.CreateOptions{},
	)
	return err
}

// GetWorkspaceURL returns the URL of an existing consumer workspace.
func (p *Provisioner) GetWorkspaceURL(ctx context.Context, name string) (string, error) {
	workspace, err := p.GetWorkspace(ctx, name)
	if err != nil {
		return "", err
	}
	if workspace.Spec.URL == "" {
		return "", fmt.Errorf("workspace %q has no URL (not ready yet?)", name)
	}
	return workspace.Spec.URL, nil
}

// GetWorkspace returns an existing consumer workspace object.
func (p *Provisioner) GetWorkspace(
	ctx context.Context,
	name string,
) (*kcptenancyv1alpha1.Workspace, error) {
	consumersClient, err := p.kcpClientForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return nil, fmt.Errorf("creating consumers client: %w", err)
	}
	workspace, err := consumersClient.TenancyV1alpha1().Workspaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting workspace %q: %w", name, err)
	}
	return workspace, nil
}

// ListWorkspaces returns all consumer workspaces under ConsumersWorkspace.
func (p *Provisioner) ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	list, err := p.listConsumerWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	result := make([]WorkspaceInfo, 0, len(list.Items))
	for _, workspace := range list.Items {
		phase := string(workspace.Status.Phase)
		info := WorkspaceInfo{
			Name:        workspace.Name,
			Phase:       phase,
			Status:      phase,
			StatusClass: "warning text-dark",
			URL:         workspace.Spec.URL,
		}

		if workspace.DeletionTimestamp != nil {
			phase = "Terminating"
			info.Phase = phase
			info.Status = "Terminating"
			info.StatusClass = "secondary"
			info.Transient = true
			info.Status, info.StatusDetail = p.logicalClusterDeletionStatus(ctx, workspace)
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
			info.DatabaseCount = p.countDatabases(ctx, p.ConsumersWorkspace+":"+workspace.Name)
		} else if phase != "" {
			info.Transient = IsTransientPhase(phase)
		} else {
			info.Status = "—"
		}

		result = append(result, info)
	}
	return result, nil
}

func (p *Provisioner) listConsumerWorkspaces(
	ctx context.Context,
) (*kcptenancyv1alpha1.WorkspaceList, error) {
	consumersClient, err := p.kcpClientForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return nil, fmt.Errorf("creating consumers client: %w", err)
	}
	return consumersClient.TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
}

func (p *Provisioner) logicalClusterDeletionStatus(
	ctx context.Context,
	workspace kcptenancyv1alpha1.Workspace,
) (string, string) {
	clusterName := workspace.Spec.Cluster
	if clusterName == "" {
		return "Finalizing parent", "Waiting for workspace cleanup after scheduling metadata removal."
	}

	cfg, err := p.configForWorkspace(clusterName)
	if err != nil {
		return "Terminating", ""
	}
	client, err := kcpclientset.NewForConfig(cfg)
	if err != nil {
		return "Terminating", ""
	}

	cluster, err := client.CoreV1alpha1().LogicalClusters().Get(ctx, kcpcorev1alpha1.LogicalClusterName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "Finalizing parent", "LogicalCluster is gone; waiting for the parent Workspace object to be removed."
		}
		return "Terminating", ""
	}

	for _, condition := range cluster.Status.Conditions {
		if condition.Type != kcptenancyv1alpha1.WorkspaceContentDeleted {
			continue
		}
		if condition.Status == "True" {
			return "Finalizing parent", "Workspace content is deleted; waiting for the parent Workspace object to be removed."
		}
		if condition.Message != "" {
			return "Deleting content", condition.Message
		}
		return "Deleting content", ""
	}

	if cluster.DeletionTimestamp != nil && !cluster.DeletionTimestamp.IsZero() {
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
	consumersClient, err := p.kcpClientForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return fmt.Errorf("creating consumers client: %w", err)
	}
	if err := consumersClient.TenancyV1alpha1().Workspaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
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
	workspaces, err := p.listConsumerWorkspaces(ctx)
	if err != nil {
		slog.Error("workspace binding reconcile: list workspaces", "err", err)
		return
	}

	for _, workspace := range workspaces.Items {
		if workspace.DeletionTimestamp != nil || IsTransientPhase(string(workspace.Status.Phase)) {
			continue
		}

		wsPath := p.ConsumersWorkspace + ":" + workspace.Name
		client, err := p.kcpClientForWorkspace(wsPath)
		if err != nil {
			slog.Error("workspace binding reconcile: client workspace",
				"workspace", workspace.Name, "err", err)
			continue
		}
		if err := p.ensureWorkspaceBinding(ctx, client); err != nil {
			slog.Error("workspace binding reconcile: ensure binding",
				"workspace", workspace.Name, "err", err)
		}
		if p.workspaceCredentialMode(workspace) != workspaceCredentialsScoped {
			continue
		}
		if err := p.ensureScopedWorkspaceCredentials(ctx, wsPath); err != nil {
			slog.Error("workspace binding reconcile: ensure scoped credentials",
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

	workspaces, err := p.listConsumerWorkspaces(ctx)
	if err != nil {
		slog.Error("headlamp sync: list consumer workspaces", "err", err)
		return
	}

	desiredWorkspaces := make(map[string]kcptenancyv1alpha1.Workspace, len(workspaces.Items))
	for _, workspace := range workspaces.Items {
		if workspace.DeletionTimestamp != nil {
			continue
		}
		desiredWorkspaces[workspace.Name] = workspace
	}

	p.removeStaleHeadlampWorkspaces(cfg, desiredWorkspaces)

	if add && wsName != "" {
		workspace, err := p.GetWorkspace(ctx, wsName)
		if err == nil && workspace.DeletionTimestamp == nil {
			desiredWorkspaces[wsName] = *workspace
		}
	} else if !add && wsName != "" {
		delete(desiredWorkspaces, wsName)
	}

	for workspaceName, workspace := range desiredWorkspaces {
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
		if p.workspaceCredentialMode(workspace) == workspaceCredentialsScoped {
			token, err := p.workspaceServiceAccountToken(ctx, p.ConsumersWorkspace+":"+workspaceName)
			if err != nil {
				slog.Error("headlamp sync: read workspace token",
					"workspace", workspaceName, "err", err)
				continue
			}
			authInfo.Token = token
		} else {
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

func (p *Provisioner) removeStaleHeadlampWorkspaces(
	cfg *clientcmdapi.Config,
	desiredWorkspaces map[string]kcptenancyv1alpha1.Workspace,
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

// KubeconfigBytes generates a kubeconfig YAML for the given workspace.
// Newly provisioned workspaces use a workspace-scoped service account token.
// Existing workspaces continue to use the admin-derived config until migrated.
func (p *Provisioner) KubeconfigBytes(
	ctx context.Context,
	workspace *kcptenancyv1alpha1.Workspace,
) ([]byte, error) {
	cluster := clientcmdapi.NewCluster()
	cluster.Server = workspace.Spec.URL
	cluster.InsecureSkipTLSVerify = p.AdminConfig.Insecure
	if !cluster.InsecureSkipTLSVerify {
		cluster.CertificateAuthorityData = p.AdminConfig.CAData
	}

	authInfo := clientcmdapi.NewAuthInfo()
	if p.workspaceCredentialMode(*workspace) == workspaceCredentialsScoped {
		token, err := p.workspaceServiceAccountToken(
			ctx, p.ConsumersWorkspace+":"+workspace.Name,
		)
		if err != nil {
			return nil, err
		}
		authInfo.Token = token
	} else {
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
	}

	kubeCtx := clientcmdapi.NewContext()
	kubeCtx.Cluster = "kcp"
	if p.workspaceCredentialMode(*workspace) == workspaceCredentialsScoped {
		kubeCtx.AuthInfo = workspaceServiceAccountName
	} else {
		kubeCtx.AuthInfo = "admin"
	}

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kcp"] = cluster
	cfg.AuthInfos[kubeCtx.AuthInfo] = authInfo
	cfg.Contexts["workspace"] = kubeCtx
	cfg.CurrentContext = "workspace"

	return clientcmd.Write(*cfg)
}

// AdminKubeconfigBytes generates a root workspace kubeconfig using the same
// external KCP base URL tenant workspace kubeconfigs use.
func (p *Provisioner) AdminKubeconfigBytes(ctx context.Context) ([]byte, error) {
	baseURL, err := p.externalKCPBaseURL(ctx)
	if err != nil {
		return nil, err
	}

	cluster := clientcmdapi.NewCluster()
	cluster.Server = baseURL + "/clusters/root"
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
	kubeCtx.Cluster = "root"
	kubeCtx.AuthInfo = "root"

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["root"] = cluster
	cfg.AuthInfos["root"] = authInfo
	cfg.Contexts["root"] = kubeCtx
	cfg.CurrentContext = "root"

	return clientcmd.Write(*cfg)
}

func (p *Provisioner) externalKCPBaseURL(ctx context.Context) (string, error) {
	parentPath, workspaceName, found := strings.Cut(p.ConsumersWorkspace, ":")
	if !found {
		return "", fmt.Errorf("consumers workspace %q is not nested under a parent workspace", p.ConsumersWorkspace)
	}

	parentClient, err := p.kcpClientForWorkspace(parentPath)
	if err != nil {
		return "", fmt.Errorf("creating parent workspace client: %w", err)
	}

	workspace, err := parentClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting consumers workspace %q: %w", p.ConsumersWorkspace, err)
	}
	if workspace.Spec.URL == "" {
		return "", fmt.Errorf("workspace %q has no URL (not ready yet?)", p.ConsumersWorkspace)
	}

	baseURL, err := kcpBaseURL(workspace.Spec.URL)
	if err != nil {
		return "", err
	}
	return baseURL, nil
}

func (p *Provisioner) ensureScopedWorkspaceCredentials(
	ctx context.Context,
	wsPath string,
) error {
	client, err := p.kubeClientForWorkspace(wsPath)
	if err != nil {
		return fmt.Errorf("creating workspace kube client: %w", err)
	}

	if err := p.ensureWorkspaceNamespace(ctx, client); err != nil {
		return err
	}

	if _, err := client.CoreV1().ServiceAccounts(workspaceDefaultNamespace).Get(
		ctx, workspaceServiceAccountName, metav1.GetOptions{},
	); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting service account: %w", err)
		}
		if _, err := client.CoreV1().ServiceAccounts(workspaceDefaultNamespace).Create(
			ctx,
			&corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceServiceAccountName,
					Namespace: workspaceDefaultNamespace,
				},
			},
			metav1.CreateOptions{},
		); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating service account: %w", err)
		}
	}

	if _, err := client.CoreV1().Secrets(workspaceDefaultNamespace).Get(
		ctx, workspaceTokenSecretName, metav1.GetOptions{},
	); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting token secret: %w", err)
		}
		if _, err := client.CoreV1().Secrets(workspaceDefaultNamespace).Create(
			ctx,
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceTokenSecretName,
					Namespace: workspaceDefaultNamespace,
					Annotations: map[string]string{
						corev1.ServiceAccountNameKey: workspaceServiceAccountName,
					},
				},
				Type: corev1.SecretTypeServiceAccountToken,
			},
			metav1.CreateOptions{},
		); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating token secret: %w", err)
		}
	}

	desiredSubject := rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Namespace: workspaceDefaultNamespace,
		Name:      workspaceServiceAccountName,
	}
	desiredBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: workspaceClusterRoleBindName},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
		Subjects: []rbacv1.Subject{desiredSubject},
	}

	binding, err := client.RbacV1().ClusterRoleBindings().Get(
		ctx, workspaceClusterRoleBindName, metav1.GetOptions{},
	)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting cluster role binding: %w", err)
		}
		if _, err := client.RbacV1().ClusterRoleBindings().Create(
			ctx, desiredBinding, metav1.CreateOptions{},
		); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating cluster role binding: %w", err)
		}
	} else if len(binding.Subjects) != 1 ||
		binding.Subjects[0] != desiredSubject ||
		binding.RoleRef != desiredBinding.RoleRef {
		binding.Subjects = desiredBinding.Subjects
		binding.RoleRef = desiredBinding.RoleRef
		if _, err := client.RbacV1().ClusterRoleBindings().Update(
			ctx, binding, metav1.UpdateOptions{},
		); err != nil {
			return fmt.Errorf("updating cluster role binding: %w", err)
		}
	}

	if _, err := p.workspaceServiceAccountToken(ctx, wsPath); err != nil {
		return err
	}

	return nil
}

func (p *Provisioner) ensureWorkspaceNamespace(
	ctx context.Context,
	client kubernetes.Interface,
) error {
	if _, err := client.CoreV1().Namespaces().Get(
		ctx, workspaceDefaultNamespace, metav1.GetOptions{},
	); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting namespace %q: %w", workspaceDefaultNamespace, err)
		}
		if _, err := client.CoreV1().Namespaces().Create(
			ctx,
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: workspaceDefaultNamespace},
			},
			metav1.CreateOptions{},
		); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating namespace %q: %w", workspaceDefaultNamespace, err)
		}
	}
	return nil
}

func (p *Provisioner) workspaceServiceAccountToken(
	ctx context.Context,
	wsPath string,
) (string, error) {
	client, err := p.kubeClientForWorkspace(wsPath)
	if err != nil {
		return "", fmt.Errorf("creating workspace kube client: %w", err)
	}

	var token string
	if err := wait.PollUntilContextTimeout(
		ctx, time.Second, 30*time.Second, false,
		func(ctx context.Context) (bool, error) {
			secret, err := client.CoreV1().Secrets(workspaceDefaultNamespace).Get(
				ctx, workspaceTokenSecretName, metav1.GetOptions{},
			)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			rawToken := secret.Data["token"]
			if len(rawToken) == 0 {
				return false, nil
			}
			token = string(rawToken)
			return true, nil
		},
	); err != nil {
		return "", fmt.Errorf("waiting for workspace token secret: %w", err)
	}

	return token, nil
}

func (p *Provisioner) markWorkspaceScopedCredentials(
	ctx context.Context,
	consumersClient kcpclientset.Interface,
	name string,
) error {
	workspace, err := consumersClient.TenancyV1alpha1().Workspaces().Get(
		ctx, name, metav1.GetOptions{},
	)
	if err != nil {
		return err
	}
	if workspace.Annotations == nil {
		workspace.Annotations = map[string]string{}
	}
	workspace.Annotations[workspaceCredentialAnnotation] = "true"
	_, err = consumersClient.TenancyV1alpha1().Workspaces().Update(
		ctx, workspace, metav1.UpdateOptions{},
	)
	return err
}

func (p *Provisioner) workspaceCredentialMode(
	workspace kcptenancyv1alpha1.Workspace,
) workspaceCredentialMode {
	if workspace.Annotations[workspaceCredentialAnnotation] == "true" {
		return workspaceCredentialsScoped
	}
	return workspaceCredentialsAdmin
}
