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

package provisioner

import (
	"context"
	"fmt"

	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// IsTransientPhase reports whether the given workspace phase is not yet stable.
func IsTransientPhase(phase string) bool {
	switch phase {
	case "Ready", "":
		return false
	}
	return true
}

// ProvisionWorkspace creates a new consumer workspace. The workspace will not
// be ready immediately; the background reconciler completes the setup once the
// workspace reaches the Ready phase.
func (p *Provisioner) ProvisionWorkspace(ctx context.Context, name string) error {
	consumersClient, err := p.kcpClientForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return fmt.Errorf("creating consumers client: %w", err)
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
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating workspace %q: %w", name, err)
	}
	return nil
}

// EnsureWorkspaceSetup runs the idempotent post-creation setup for a consumer
// workspace: APIBindings, scoped credentials, and headlamp sync.
func (p *Provisioner) EnsureWorkspaceSetup(ctx context.Context, name string) error {
	consumersClient, err := p.kcpClientForWorkspace(p.ConsumersWorkspace)
	if err != nil {
		return fmt.Errorf("creating consumers client: %w", err)
	}

	wsPath := p.ConsumersWorkspace + ":" + name
	wsClient, err := p.kcpClientForWorkspace(wsPath)
	if err != nil {
		return fmt.Errorf("creating workspace client: %w", err)
	}
	if err := p.ensureWorkspaceBindings(ctx, wsClient); err != nil {
		return fmt.Errorf("creating APIBindings in workspace %q: %w", name, err)
	}
	if err := p.ensureScopedWorkspaceCredentials(ctx, wsPath); err != nil {
		return fmt.Errorf("creating scoped credentials in workspace %q: %w", name, err)
	}
	if err := p.markWorkspaceScopedCredentials(ctx, consumersClient, name); err != nil {
		return fmt.Errorf("marking workspace %q for scoped credentials: %w", name, err)
	}

	go p.syncHeadlamp(p.ProcessContext, name, true)
	return nil
}

// GetWorkspace returns an existing consumer workspace object.
func (p *Provisioner) GetWorkspace(ctx context.Context, name string) (*kcptenancyv1alpha1.Workspace, error) {
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

// ListWorkspaces returns display info for all consumer workspaces.
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
			info.Phase = "Terminating"
			info.Status, info.StatusDetail = p.logicalClusterDeletionStatus(ctx, workspace)
			info.Transient = true
			switch info.Status {
			case "Finalizing parent":
				info.StatusClass = "info"
			default:
				info.StatusClass = "secondary"
			}
			result = append(result, info)
			continue
		}

		switch phase {
		case "Ready":
			if p.workspaceCredentialMode(workspace) != workspaceCredentialsScoped {
				info.Status = "Provisioning"
				info.StatusClass = "warning text-dark"
				info.Transient = true
			} else {
				info.Status = "Ready"
				info.StatusClass = "success"
				info.DatabaseCount = p.countDatabases(ctx, p.ConsumersWorkspace+":"+workspace.Name)
			}
		case "":
			info.Status = "—"
		default:
			info.Transient = IsTransientPhase(phase)
		}

		result = append(result, info)
	}
	return result, nil
}

// DeleteWorkspace deletes a consumer workspace.
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

func (p *Provisioner) listConsumerWorkspaces(ctx context.Context) (*kcptenancyv1alpha1.WorkspaceList, error) {
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
