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
	"log/slog"

	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReconcileWorkspaceBindings ensures all non-terminating consumer workspaces have
// the expected tenant APIBindings. Safe to call periodically.
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

		if err := p.EnsureWorkspaceSetup(ctx, workspace.Name); err != nil {
			slog.Error("workspace reconcile: ensure setup",
				"workspace", workspace.Name, "err", err)
		}
	}
}

func (p *Provisioner) ensureWorkspaceBindings(ctx context.Context, wsClient kcpclientset.Interface) error {
	for _, binding := range p.Bindings {
		if _, err := wsClient.ApisV1alpha2().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{}); err == nil {
			continue
		} else if !apierrors.IsNotFound(err) {
			return err
		}
		if _, err := wsClient.ApisV1alpha2().APIBindings().Create(
			ctx, p.desiredBinding(binding), metav1.CreateOptions{},
		); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provisioner) desiredBinding(binding WorkspaceBinding) *kcpapisv1alpha2.APIBinding {
	return &kcpapisv1alpha2.APIBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apis.kcp.io/v1alpha2",
			Kind:       "APIBinding",
		},
		ObjectMeta: metav1.ObjectMeta{Name: binding.Name},
		Spec: kcpapisv1alpha2.APIBindingSpec{
			Reference: kcpapisv1alpha2.BindingReference{
				Export: &kcpapisv1alpha2.ExportBindingReference{
					Name: binding.ExportName,
					Path: p.ProviderWorkspace,
				},
			},
			PermissionClaims: binding.PermissionClaims,
		},
	}
}
