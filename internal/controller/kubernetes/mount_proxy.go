package kubernetes

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type MountProxy struct {
	K8sClient kubernetes.Interface
}

func (p *MountProxy) Handler() http.Handler {
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
