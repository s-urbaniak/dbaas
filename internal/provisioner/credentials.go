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
	"time"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

func (p *Provisioner) workspaceCredentialMode(workspace kcptenancyv1alpha1.Workspace) workspaceCredentialMode {
	if workspace.Annotations[workspaceCredentialAnnotation] == "true" {
		return workspaceCredentialsScoped
	}
	return workspaceCredentialsAdmin
}

func (p *Provisioner) markWorkspaceScopedCredentials(
	ctx context.Context,
	consumersClient kcpclientset.Interface,
	name string,
) error {
	workspace, err := consumersClient.TenancyV1alpha1().Workspaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if workspace.Annotations == nil {
		workspace.Annotations = map[string]string{}
	}
	workspace.Annotations[workspaceCredentialAnnotation] = "true"
	_, err = consumersClient.TenancyV1alpha1().Workspaces().Update(ctx, workspace, metav1.UpdateOptions{})
	return err
}

func (p *Provisioner) ensureScopedWorkspaceCredentials(ctx context.Context, wsPath string) error {
	client, err := p.kubeClientForWorkspace(wsPath)
	if err != nil {
		return fmt.Errorf("creating workspace kube client: %w", err)
	}

	if err := p.ensureWorkspaceNamespace(ctx, client); err != nil {
		return err
	}
	if err := p.ensureServiceAccount(ctx, client); err != nil {
		return err
	}
	if err := p.ensureTokenSecret(ctx, client); err != nil {
		return err
	}
	if err := p.ensureAdminClusterRoleBinding(ctx, client); err != nil {
		return err
	}

	// Block until the token is populated.
	if _, err := p.workspaceServiceAccountToken(ctx, wsPath); err != nil {
		return err
	}
	return nil
}

func (p *Provisioner) ensureWorkspaceNamespace(ctx context.Context, client kubernetes.Interface) error {
	_, err := client.CoreV1().Namespaces().Get(ctx, workspaceDefaultNamespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting namespace %q: %w", workspaceDefaultNamespace, err)
	}
	_, err = client.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: workspaceDefaultNamespace}},
		metav1.CreateOptions{},
	)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %q: %w", workspaceDefaultNamespace, err)
	}
	return nil
}

func (p *Provisioner) ensureServiceAccount(ctx context.Context, client kubernetes.Interface) error {
	_, err := client.CoreV1().ServiceAccounts(workspaceDefaultNamespace).Get(
		ctx, workspaceServiceAccountName, metav1.GetOptions{},
	)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting service account: %w", err)
	}
	_, err = client.CoreV1().ServiceAccounts(workspaceDefaultNamespace).Create(ctx,
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workspaceServiceAccountName,
				Namespace: workspaceDefaultNamespace,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating service account: %w", err)
	}
	return nil
}

func (p *Provisioner) ensureTokenSecret(ctx context.Context, client kubernetes.Interface) error {
	_, err := client.CoreV1().Secrets(workspaceDefaultNamespace).Get(
		ctx, workspaceTokenSecretName, metav1.GetOptions{},
	)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting token secret: %w", err)
	}
	_, err = client.CoreV1().Secrets(workspaceDefaultNamespace).Create(ctx,
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
	)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating token secret: %w", err)
	}
	return nil
}

func (p *Provisioner) ensureAdminClusterRoleBinding(ctx context.Context, client kubernetes.Interface) error {
	desired := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: workspaceClusterRoleBindName},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Namespace: workspaceDefaultNamespace,
			Name:      workspaceServiceAccountName,
		}},
	}

	existing, err := client.RbacV1().ClusterRoleBindings().Get(ctx, workspaceClusterRoleBindName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().ClusterRoleBindings().Create(ctx, desired, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating cluster role binding: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting cluster role binding: %w", err)
	}

	if len(existing.Subjects) == 1 &&
		existing.Subjects[0] == desired.Subjects[0] &&
		existing.RoleRef == desired.RoleRef {
		return nil
	}
	existing.Subjects = desired.Subjects
	existing.RoleRef = desired.RoleRef
	if _, err := client.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating cluster role binding: %w", err)
	}
	return nil
}

func (p *Provisioner) workspaceServiceAccountToken(ctx context.Context, wsPath string) (string, error) {
	client, err := p.kubeClientForWorkspace(wsPath)
	if err != nil {
		return "", fmt.Errorf("creating workspace kube client: %w", err)
	}

	var token string
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, false,
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
			if raw := secret.Data["token"]; len(raw) > 0 {
				token = string(raw)
				return true, nil
			}
			return false, nil
		},
	); err != nil {
		return "", fmt.Errorf("waiting for workspace token secret: %w", err)
	}
	return token, nil
}
