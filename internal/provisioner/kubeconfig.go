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
	"net"
	"net/url"
	"strings"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubeconfigBytes generates a kubeconfig YAML for the given workspace.
// Workspaces annotated for scoped credentials use a workspace service account token;
// others fall back to the admin credentials.
func (p *Provisioner) KubeconfigBytes(ctx context.Context, workspace *kcptenancyv1alpha1.Workspace) ([]byte, error) {
	return p.KubeconfigBytesForExternalBaseURL(ctx, workspace, "")
}

// KubeconfigBytesForExternalBaseURL generates a kubeconfig YAML for the given workspace,
// optionally replacing the host:port portion with the supplied base URL.
func (p *Provisioner) KubeconfigBytesForExternalBaseURL(
	ctx context.Context,
	workspace *kcptenancyv1alpha1.Workspace,
	externalBaseURL string,
) ([]byte, error) {
	serverURL, err := externalURLForServer(workspace.Spec.URL, externalBaseURL)
	if err != nil {
		return nil, err
	}

	cluster := clientcmdapi.NewCluster()
	cluster.Server = serverURL
	cluster.InsecureSkipTLSVerify = p.AdminConfig.Insecure
	if !cluster.InsecureSkipTLSVerify {
		cluster.CertificateAuthorityData = p.AdminConfig.CAData
	}

	var authInfo *clientcmdapi.AuthInfo
	var authInfoName string
	if p.workspaceCredentialMode(*workspace) == workspaceCredentialsScoped {
		token, err := p.workspaceServiceAccountToken(ctx, p.ConsumersWorkspace+":"+workspace.Name)
		if err != nil {
			return nil, err
		}
		info := clientcmdapi.NewAuthInfo()
		info.Token = token
		authInfo = info
		authInfoName = workspaceServiceAccountName
	} else {
		authInfo = p.adminAuthInfo()
		authInfoName = "admin"
	}

	kubeCtx := clientcmdapi.NewContext()
	kubeCtx.Cluster = "kcp"
	kubeCtx.AuthInfo = authInfoName

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kcp"] = cluster
	cfg.AuthInfos[authInfoName] = authInfo
	cfg.Contexts["workspace"] = kubeCtx
	cfg.CurrentContext = "workspace"

	return clientcmd.Write(*cfg)
}

// AdminKubeconfigBytes generates a root workspace kubeconfig using the external kcp base URL.
func (p *Provisioner) AdminKubeconfigBytes(ctx context.Context) ([]byte, error) {
	return p.AdminKubeconfigBytesForExternalBaseURL(ctx, "")
}

// AdminKubeconfigBytesForExternalBaseURL generates a root workspace kubeconfig,
// optionally overriding the externally reachable base URL.
func (p *Provisioner) AdminKubeconfigBytesForExternalBaseURL(ctx context.Context, externalBaseURL string) ([]byte, error) {
	baseURL := externalBaseURL
	if baseURL == "" {
		var err error
		baseURL, err = p.externalKCPBaseURL(ctx)
		if err != nil {
			return nil, err
		}
	}

	cluster := clientcmdapi.NewCluster()
	cluster.Server = baseURL + "/clusters/root"
	cluster.InsecureSkipTLSVerify = p.AdminConfig.Insecure
	if !cluster.InsecureSkipTLSVerify {
		cluster.CertificateAuthorityData = p.AdminConfig.CAData
	}

	kubeCtx := clientcmdapi.NewContext()
	kubeCtx.Cluster = "root"
	kubeCtx.AuthInfo = "root"

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["root"] = cluster
	cfg.AuthInfos["root"] = p.adminAuthInfo()
	cfg.Contexts["root"] = kubeCtx
	cfg.CurrentContext = "root"

	return clientcmd.Write(*cfg)
}

// externalKCPBaseURL resolves the public base URL of the kcp server by reading
// the consumers workspace URL via the admin config.
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

	return kcpBaseURL(workspace.Spec.URL)
}

func externalURLForServer(serverURL, externalBaseURL string) (string, error) {
	if externalBaseURL == "" {
		return serverURL, nil
	}
	server, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parsing kcp server URL %q: %w", serverURL, err)
	}
	base, err := url.Parse(externalBaseURL)
	if err != nil {
		return "", fmt.Errorf("parsing external kcp base URL %q: %w", externalBaseURL, err)
	}
	server.Scheme = base.Scheme
	server.Host = base.Host
	return server.String(), nil
}

// ExternalBaseURLForHost builds a base URL from an HTTP host header value,
// a scheme, and a port override.
func ExternalBaseURLForHost(host, scheme, port string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("host is required")
	}
	if scheme == "" {
		return "", fmt.Errorf("scheme is required")
	}
	if port == "" {
		return "", fmt.Errorf("port is required")
	}
	cleanHost, err := hostWithoutPort(host)
	if err != nil {
		return "", err
	}
	return (&url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(cleanHost, port),
	}).String(), nil
}

func hostWithoutPort(hostport string) (string, error) {
	hostport = strings.TrimSpace(strings.Trim(hostport, `"`))
	if hostport == "" {
		return "", fmt.Errorf("host is required")
	}
	if strings.Contains(hostport, "://") {
		u, err := url.Parse(hostport)
		if err != nil {
			return "", fmt.Errorf("parsing host %q: %w", hostport, err)
		}
		hostport = u.Host
	}
	if strings.HasPrefix(hostport, "[") {
		host, port, err := net.SplitHostPort(hostport)
		if err == nil && port != "" {
			return host, nil
		}
		return strings.Trim(hostport, "[]"), nil
	}
	if host, port, err := net.SplitHostPort(hostport); err == nil && port != "" {
		return host, nil
	}
	if strings.Count(hostport, ":") > 1 {
		return hostport, nil
	}
	return hostport, nil
}
