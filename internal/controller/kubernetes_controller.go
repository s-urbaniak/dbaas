package controller

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	kcpapis "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var kubernetesGVK = schema.GroupVersionKind{
	Group:   "kro.run",
	Version: "v1alpha1",
	Kind:    "Kubernetes",
}

var capiClusterGVK = schema.GroupVersionKind{
	Group:   "cluster.x-k8s.io",
	Version: "v1beta2",
	Kind:    "Cluster",
}

const kubernetesFinalizer = "dbaas.mongodb.com/kubernetes-mount"
const controlPlaneNoScheduleTaintKey = "node-role.kubernetes.io/control-plane"

type KubernetesReconciler struct {
	client.Client

	K8sClient          kubernetes.Interface
	KCPConfig          *rest.Config
	ConsumersWorkspace string
	ProxyBaseURL       string
}

func (r *KubernetesReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(kubernetesGVK)
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if obj.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, obj)
	}

	if !containsString(obj.GetFinalizers(), kubernetesFinalizer) {
		patch := client.MergeFrom(obj.DeepCopy())
		obj.SetFinalizers(append(obj.GetFinalizers(), kubernetesFinalizer))
		if err := r.Patch(ctx, obj, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	log.FromContext(ctx).V(1).Info("reconciling Kubernetes", "name", req.NamespacedName.String())

	remoteName := firstNonEmpty(obj.GetAnnotations()["syncagent.kcp.io/remote-object-name"], obj.GetName())
	remoteNamespace := firstNonEmpty(obj.GetAnnotations()["syncagent.kcp.io/remote-object-namespace"], "default")
	mountedWorkspace := deriveMountedWorkspaceName(remoteName, remoteNamespace)

	clusterReady, phase, err := r.workloadClusterPhase(ctx, obj.GetNamespace(), obj.GetName())
	if err != nil {
		return ctrl.Result{}, err
	}

	statusURL := ""
	kubeconfigReady, err := r.clusterKubeconfigExists(ctx, obj.GetNamespace(), obj.GetName())
	if err != nil {
		return ctrl.Result{}, err
	}
	if kubeconfigReady {
		if err := r.ensureCalicoInstalled(ctx, obj.GetNamespace(), obj.GetName()); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileControlPlaneScheduling(ctx, obj); err != nil {
			return ctrl.Result{}, err
		}
	}
	if clusterReady && kubeconfigReady {
		statusURL = fmt.Sprintf("%s/mounts/%s/%s", strings.TrimSuffix(r.ProxyBaseURL, "/"), obj.GetNamespace(), obj.GetName())
		phase = "Ready"
		if err := r.ensureMountedWorkspace(ctx, obj.GetNamespace(), mountedWorkspace, remoteName, remoteNamespace); err != nil {
			return ctrl.Result{}, err
		}
	} else if clusterReady {
		phase = "Connecting"
	}

	if err := r.patchKubernetesStatus(ctx, obj, phase, clusterReady && kubeconfigReady, statusURL, mountedWorkspace); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func (r *KubernetesReconciler) reconcileDelete(ctx context.Context, obj *unstructured.Unstructured) (ctrl.Result, error) {
	if !containsString(obj.GetFinalizers(), kubernetesFinalizer) {
		return ctrl.Result{}, nil
	}

	remoteName := firstNonEmpty(obj.GetAnnotations()["syncagent.kcp.io/remote-object-name"], obj.GetName())
	remoteNamespace := firstNonEmpty(obj.GetAnnotations()["syncagent.kcp.io/remote-object-namespace"], "default")
	mountedWorkspace := deriveMountedWorkspaceName(remoteName, remoteNamespace)

	if err := r.deleteMountedWorkspace(ctx, obj.GetNamespace(), mountedWorkspace); err != nil {
		return ctrl.Result{}, err
	}

	patch := client.MergeFrom(obj.DeepCopy())
	obj.SetFinalizers(removeString(obj.GetFinalizers(), kubernetesFinalizer))
	if err := r.Patch(ctx, obj, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *KubernetesReconciler) SetupWithManager(mgr ctrl.Manager) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(kubernetesGVK)
	return ctrl.NewControllerManagedBy(mgr).For(obj).Complete(r)
}

func (r *KubernetesReconciler) patchKubernetesStatus(
	ctx context.Context,
	obj *unstructured.Unstructured,
	phase string,
	ready bool,
	statusURL string,
	mountedWorkspace string,
) error {
	patch := client.MergeFrom(obj.DeepCopy())
	if err := unstructured.SetNestedField(obj.Object, phase, "status", "phase"); err != nil {
		return fmt.Errorf("setting status.phase: %w", err)
	}
	if err := unstructured.SetNestedField(obj.Object, ready, "status", "ready"); err != nil {
		return fmt.Errorf("setting status.ready: %w", err)
	}
	if err := unstructured.SetNestedField(obj.Object, statusURL, "status", "URL"); err != nil {
		return fmt.Errorf("setting status.URL: %w", err)
	}
	if err := unstructured.SetNestedField(obj.Object, mountedWorkspace, "status", "mountedWorkspace"); err != nil {
		return fmt.Errorf("setting status.mountedWorkspace: %w", err)
	}
	if err := r.Status().Patch(ctx, obj, patch); err != nil {
		return fmt.Errorf("patching Kubernetes status: %w", err)
	}
	return nil
}

func (r *KubernetesReconciler) workloadClusterPhase(
	ctx context.Context,
	namespace string,
	name string,
) (ready bool, phase string, err error) {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(capiClusterGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "Initializing", nil
		}
		return false, "", fmt.Errorf("getting CAPI Cluster %s/%s: %w", namespace, name, err)
	}

	conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if condition["type"] == "Available" && condition["status"] == "True" {
			return true, "Ready", nil
		}
	}

	if clusterPhase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase"); clusterPhase != "" {
		return false, "Connecting", nil
	}
	return false, "Initializing", nil
}

func (r *KubernetesReconciler) clusterKubeconfigExists(ctx context.Context, namespace string, name string) (bool, error) {
	_, err := r.K8sClient.CoreV1().Secrets(namespace).Get(ctx, name+"-kubeconfig", metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("getting kubeconfig secret for %s/%s: %w", namespace, name, err)
}

func (r *KubernetesReconciler) ensureCalicoInstalled(ctx context.Context, namespace string, name string) error {
	manifestConfigMap, err := r.K8sClient.CoreV1().ConfigMaps("default").Get(ctx, "dbaas-calico", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting dbaas-calico configmap: %w", err)
	}
	manifest, ok := manifestConfigMap.Data["calico.yaml"]
	if !ok || manifest == "" {
		return fmt.Errorf("configmap dbaas-calico is missing calico.yaml")
	}

	cfg, err := r.workloadRESTConfig(ctx, namespace, name)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building workload dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building workload discovery client: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))

	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	for {
		obj := map[string]interface{}{}
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decoding calico manifest: %w", err)
		}
		if len(obj) == 0 {
			continue
		}

		u := &unstructured.Unstructured{Object: obj}
		gvk := u.GroupVersionKind()
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return fmt.Errorf("mapping %s: %w", gvk.String(), err)
		}
		body, err := json.Marshal(u.Object)
		if err != nil {
			return fmt.Errorf("marshalling %s %s: %w", gvk.Kind, u.GetName(), err)
		}

		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			if _, err := dyn.Resource(mapping.Resource).Namespace(u.GetNamespace()).Patch(
				ctx,
				u.GetName(),
				types.ApplyPatchType,
				body,
				metav1.PatchOptions{
					FieldManager: "dbaas-kubernetes-controller",
					Force:        boolPtr(true),
				},
			); err != nil {
				return fmt.Errorf("applying %s %s: %w", gvk.Kind, u.GetName(), err)
			}
			continue
		}
		if _, err := dyn.Resource(mapping.Resource).Patch(
			ctx,
			u.GetName(),
			types.ApplyPatchType,
			body,
			metav1.PatchOptions{
				FieldManager: "dbaas-kubernetes-controller",
				Force:        boolPtr(true),
			},
		); err != nil {
			return fmt.Errorf("applying %s %s: %w", gvk.Kind, u.GetName(), err)
		}
	}

	return nil
}

func (r *KubernetesReconciler) reconcileControlPlaneScheduling(ctx context.Context, obj *unstructured.Unstructured) error {
	allowScheduling, found, err := unstructured.NestedBool(obj.Object, "spec", "allowSchedulingOnControlPlanes")
	if err != nil {
		return fmt.Errorf("reading spec.allowSchedulingOnControlPlanes: %w", err)
	}
	if !found {
		allowScheduling = true
	}

	cfg, err := r.workloadRESTConfig(ctx, obj.GetNamespace(), obj.GetName())
	if err != nil {
		return err
	}
	workloadClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building workload kubernetes client: %w", err)
	}

	nodes, err := workloadClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/control-plane",
	})
	if err != nil {
		return fmt.Errorf("listing control-plane nodes: %w", err)
	}
	for _, node := range nodes.Items {
		updated, changed := desiredControlPlaneTaints(node.Spec.Taints, allowScheduling)
		if !changed {
			continue
		}
		nodeCopy := node.DeepCopy()
		nodeCopy.Spec.Taints = updated
		if _, err := workloadClient.CoreV1().Nodes().Update(ctx, nodeCopy, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating taints for node %s: %w", node.Name, err)
		}
	}

	return nil
}

func (r *KubernetesReconciler) workloadRESTConfig(ctx context.Context, namespace string, name string) (*rest.Config, error) {
	secret, err := r.K8sClient.CoreV1().Secrets(namespace).Get(ctx, name+"-kubeconfig", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting kubeconfig secret %s/%s-kubeconfig: %w", namespace, name, err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(secret.Data["value"])
	if err != nil {
		return nil, fmt.Errorf("building workload kubeconfig for %s/%s: %w", namespace, name, err)
	}
	return cfg, nil
}

func desiredControlPlaneTaints(taints []corev1.Taint, allowScheduling bool) ([]corev1.Taint, bool) {
	updated := make([]corev1.Taint, 0, len(taints)+1)
	changed := false
	found := false

	for _, taint := range taints {
		if taint.Key == controlPlaneNoScheduleTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
			found = true
			if allowScheduling {
				changed = true
				continue
			}
		}
		updated = append(updated, taint)
	}

	if !allowScheduling && !found {
		updated = append(updated, corev1.Taint{
			Key:    controlPlaneNoScheduleTaintKey,
			Effect: corev1.TaintEffectNoSchedule,
		})
		changed = true
	}

	return updated, changed
}

func (r *KubernetesReconciler) ensureMountedWorkspace(
	ctx context.Context,
	clusterID string,
	mountedWorkspace string,
	remoteName string,
	remoteNamespace string,
) error {
	tenantWorkspacePath, err := r.consumerWorkspacePathForCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	if tenantWorkspacePath == "" {
		return fmt.Errorf("no tenant workspace found for logical cluster %q", clusterID)
	}

	clientset, err := r.kcpClientForWorkspace(tenantWorkspacePath)
	if err != nil {
		return err
	}

	existing, err := clientset.TenancyV1alpha1().Workspaces().Get(ctx, mountedWorkspace, metav1.GetOptions{})
	if err == nil {
		if existing.Spec.Mount != nil &&
			existing.Spec.Mount.Reference.Name == remoteName &&
			existing.Spec.Mount.Reference.Namespace == remoteNamespace {
			return nil
		}
		return fmt.Errorf("workspace %q already exists in %s with a different mount reference", mountedWorkspace, tenantWorkspacePath)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting mounted workspace %q: %w", mountedWorkspace, err)
	}

	workspace := &kcpapis.Workspace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "tenancy.kcp.io/v1alpha1",
			Kind:       "Workspace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: mountedWorkspace,
		},
		Spec: kcpapis.WorkspaceSpec{
			Mount: &kcpapis.Mount{
				Reference: kcpapis.ObjectReference{
					APIVersion: "kro.run/v1alpha1",
					Kind:       "Kubernetes",
					Name:       remoteName,
					Namespace:  remoteNamespace,
				},
			},
		},
	}
	if _, err := clientset.TenancyV1alpha1().Workspaces().Create(ctx, workspace, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating mounted workspace %q in %s: %w", mountedWorkspace, tenantWorkspacePath, err)
	}
	return nil
}

func (r *KubernetesReconciler) deleteMountedWorkspace(ctx context.Context, clusterID string, mountedWorkspace string) error {
	tenantWorkspacePath, err := r.consumerWorkspacePathForCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	if tenantWorkspacePath == "" {
		return nil
	}

	clientset, err := r.kcpClientForWorkspace(tenantWorkspacePath)
	if err != nil {
		return err
	}
	if err := clientset.TenancyV1alpha1().Workspaces().Delete(ctx, mountedWorkspace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting mounted workspace %q: %w", mountedWorkspace, err)
	}
	return nil
}

func (r *KubernetesReconciler) consumerWorkspacePathForCluster(ctx context.Context, clusterID string) (string, error) {
	clientset, err := r.kcpClientForWorkspace(r.ConsumersWorkspace)
	if err != nil {
		return "", err
	}
	workspaces, err := clientset.TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("listing consumer workspaces: %w", err)
	}
	for _, workspace := range workspaces.Items {
		if workspace.Spec.Cluster == clusterID || workspace.Annotations["internal.tenancy.kcp.io/cluster"] == clusterID {
			return r.ConsumersWorkspace + ":" + workspace.Name, nil
		}
	}
	return "", nil
}

func (r *KubernetesReconciler) kcpClientForWorkspace(workspacePath string) (kcpclientset.Interface, error) {
	cfg := rest.CopyConfig(r.KCPConfig)
	baseURL, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("parsing KCP host %q: %w", cfg.Host, err)
	}
	baseURL.Path = ""
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	cfg.Host = strings.TrimSuffix(baseURL.String(), "/") + "/clusters/" + workspacePath
	return kcpclientset.NewForConfig(cfg)
}

func deriveMountedWorkspaceName(remoteName string, remoteNamespace string) string {
	if remoteNamespace == "" || remoteNamespace == "default" {
		return remoteName
	}
	sum := sha1.Sum([]byte(remoteNamespace + "/" + remoteName))
	return fmt.Sprintf("%s-%s", remoteName, hex.EncodeToString(sum[:4]))
}

type KubernetesMountProxy struct {
	K8sClient kubernetes.Interface
}

func (p *KubernetesMountProxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/mounts/")
		parts := strings.SplitN(path, "/", 3)
		if len(parts) < 3 {
			http.Error(w, "expected /mounts/<namespace>/<name>/...", http.StatusBadRequest)
			return
		}

		namespace := parts[0]
		name := parts[1]
		upstreamPath := "/" + parts[2]

		secret, err := p.K8sClient.CoreV1().Secrets(namespace).Get(r.Context(), name+"-kubeconfig", metav1.GetOptions{})
		if err != nil {
			http.Error(w, fmt.Sprintf("loading kubeconfig secret: %v", err), http.StatusBadGateway)
			return
		}
		kubeconfigData := secret.Data["value"]
		if len(kubeconfigData) == 0 {
			http.Error(w, "missing kubeconfig data in secret key value", http.StatusBadGateway)
			return
		}

		cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
		if err != nil {
			http.Error(w, fmt.Sprintf("parsing kubeconfig: %v", err), http.StatusBadGateway)
			return
		}
		target, err := url.Parse(cfg.Host)
		if err != nil {
			http.Error(w, fmt.Sprintf("parsing upstream host: %v", err), http.StatusBadGateway)
			return
		}
		transport, err := rest.TransportFor(cfg)
		if err != nil {
			http.Error(w, fmt.Sprintf("building upstream transport: %v", err), http.StatusBadGateway)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = transport
		proxy.Director = nil
		proxy.Rewrite = func(req *httputil.ProxyRequest) {
			req.Out.URL.Scheme = target.Scheme
			req.Out.URL.Host = target.Host
			req.Out.URL.Path = joinURLPath(target.Path, upstreamPath)
			req.Out.Host = target.Host
			req.SetXForwarded()
		}
		proxy.ServeHTTP(w, r)
	})
}

func joinURLPath(basePath string, requestPath string) string {
	switch {
	case strings.HasSuffix(basePath, "/") && strings.HasPrefix(requestPath, "/"):
		return basePath + strings.TrimPrefix(requestPath, "/")
	case !strings.HasSuffix(basePath, "/") && !strings.HasPrefix(requestPath, "/"):
		return basePath + "/" + requestPath
	default:
		return basePath + requestPath
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func removeString(values []string, needle string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if value != needle {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func boolPtr(value bool) *bool {
	return &value
}
