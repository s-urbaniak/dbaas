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

// Package provisioner creates and manages kcp consumer workspaces for the DBaaS demo.
package provisioner

import (
	"context"
	"fmt"
	"net/url"

	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

var mongodbDatabaseGVR = schema.GroupVersionResource{
	Group:    "kro.run",
	Version:  "v1alpha1",
	Resource: "databases",
}

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

// Provisioner creates and manages kcp consumer workspaces.
type Provisioner struct {
	// ProcessContext is canceled when the provisioner process is shutting down.
	// Used to keep background goroutines alive beyond individual HTTP requests.
	ProcessContext context.Context
	// AdminConfig is the kcp admin REST config.
	AdminConfig *rest.Config
	// ProviderWorkspace is the kcp path of the service-provider workspace (e.g. "root:dbaas-provider").
	ProviderWorkspace string
	// Bindings lists the APIBindings that every consumer workspace should have.
	Bindings []WorkspaceBinding
	// ConsumersWorkspace is the kcp path of the consumer org workspace (e.g. "root:consumers").
	ConsumersWorkspace string

	// Headlamp integration — no-op when K8sClient is nil.
	K8sClient          kubernetes.Interface
	HeadlampNamespace  string
	HeadlampSecret     string
	HeadlampDeployment string
}

// WorkspaceBinding describes one tenant APIBinding managed by the provisioner.
type WorkspaceBinding struct {
	Name             string
	ExportName       string
	PermissionClaims []kcpapisv1alpha2.AcceptablePermissionClaim
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

// kcpBaseURL strips the /clusters/... path from a kcp server URL, returning just the host+port.
func kcpBaseURL(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("parsing kcp server URL %q: %w", server, err)
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// configForWorkspace returns a REST config scoped to a specific kcp workspace path.
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

// adminAuthInfo builds a kubeconfig AuthInfo from the admin REST config credentials.
func (p *Provisioner) adminAuthInfo() *clientcmdapi.AuthInfo {
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
	return authInfo
}
