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
	"log/slog"
	"strings"
	"time"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// ReconcileHeadlamp refreshes the Headlamp workspace kubeconfig. Safe to call periodically.
func (p *Provisioner) ReconcileHeadlamp(ctx context.Context) {
	p.syncHeadlamp(ctx, "", false)
}

// syncHeadlamp adds (add=true) or removes (add=false) a workspace from the Headlamp
// kubeconfig Secret and rolling-restarts the Headlamp Deployment.
// Errors are logged but never returned — Headlamp sync is best-effort.
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
		if cfg, err = clientcmd.Load(raw); err != nil {
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

	desired := make(map[string]kcptenancyv1alpha1.Workspace, len(workspaces.Items))
	for _, ws := range workspaces.Items {
		if ws.DeletionTimestamp == nil {
			desired[ws.Name] = ws
		}
	}

	p.removeStaleHeadlampEntries(cfg, desired)

	switch {
	case add && wsName != "":
		if ws, err := p.GetWorkspace(ctx, wsName); err == nil && ws.DeletionTimestamp == nil {
			desired[wsName] = *ws
		}
	case !add && wsName != "":
		delete(desired, wsName)
	}

	for name, ws := range desired {
		cluster := clientcmdapi.NewCluster()
		cluster.Server = fmt.Sprintf(
			"https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/%s:%s",
			p.ConsumersWorkspace, name,
		)
		cluster.InsecureSkipTLSVerify = p.AdminConfig.Insecure
		if !cluster.InsecureSkipTLSVerify {
			cluster.CertificateAuthorityData = p.AdminConfig.CAData
		}

		var authInfo *clientcmdapi.AuthInfo
		if p.workspaceCredentialMode(ws) == workspaceCredentialsScoped {
			token, err := p.workspaceServiceAccountToken(ctx, p.ConsumersWorkspace+":"+name)
			if err != nil {
				slog.Error("headlamp sync: read workspace token", "workspace", name, "err", err)
				continue
			}
			info := clientcmdapi.NewAuthInfo()
			info.Token = token
			authInfo = info
		} else {
			authInfo = p.adminAuthInfo()
		}

		kubeCtx := clientcmdapi.NewContext()
		kubeCtx.Cluster = name
		kubeCtx.AuthInfo = name

		cfg.Clusters[name] = cluster
		cfg.AuthInfos[name] = authInfo
		cfg.Contexts[name] = kubeCtx
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

func (p *Provisioner) removeStaleHeadlampEntries(
	cfg *clientcmdapi.Config,
	desired map[string]kcptenancyv1alpha1.Workspace,
) {
	managedPrefix := fmt.Sprintf("/clusters/%s:", p.ConsumersWorkspace)
	for name, cluster := range cfg.Clusters {
		if !strings.Contains(cluster.Server, managedPrefix) {
			continue
		}
		if _, keep := desired[name]; keep {
			continue
		}
		delete(cfg.Clusters, name)
		delete(cfg.AuthInfos, name)
		delete(cfg.Contexts, name)
	}
}
