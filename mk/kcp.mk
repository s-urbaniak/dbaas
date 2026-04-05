HELM_KCP_CHART := kcp/kcp
HELM_KCP_NS := kcp
HELM_KCP_VALUES := deploy/kcp/kcp-values.yaml

.PHONY: deploy-kcp undeploy-kcp apply-kcp-front-proxy-cert
deploy-kcp: ## Install kcp with in-cluster front-proxy workspace-controller access
	$(KUBECTL) create namespace $(HELM_KCP_NS) \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) apply -f deploy/kcp/external-admin-kubeconfig-service.yaml
	$(HELM) upgrade --install kcp $(HELM_KCP_CHART) \
	  -n $(HELM_KCP_NS) --create-namespace \
	  -f $(HELM_KCP_VALUES)
	$(MAKE) apply-kcp-front-proxy-cert
	@echo "Waiting for kcp pods to be ready..."
	$(KUBECTL) -n $(HELM_KCP_NS) rollout status deployment/kcp --timeout=600s
	$(KUBECTL) -n $(HELM_KCP_NS) rollout status deployment/kcp-front-proxy --timeout=600s
	$(KUBECTL) apply -f deploy/kcp/admin-cert.yaml
	@echo "Waiting for kcp-admin-client-cert to be issued by cert-manager..."
	@until $(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert >/dev/null 2>&1; do sleep 2; done
	@echo "✓ kcp-admin-client-cert ready"

undeploy-kcp: ## Remove kcp from the local cluster
	$(HELM) uninstall kcp -n $(HELM_KCP_NS) || true
	$(KUBECTL) delete -f deploy/kcp/external-admin-kubeconfig-service.yaml --ignore-not-found

.PHONY: apply-kcp-front-proxy-cert
apply-kcp-front-proxy-cert: ## Reconcile the kcp front-proxy certificate with default and extra SANs
	KCP_FRONT_PROXY_EXTRA_SANS='$(KCP_FRONT_PROXY_EXTRA_SANS)' \
	  $(SCRIPTS_DIR)/render_kcp_front_proxy_cert.sh $(HELM_KCP_NS) | $(KUBECTL) apply -f -
	$(KUBECTL) -n $(HELM_KCP_NS) wait --for=condition=Ready certificate/kcp-front-proxy --timeout=600s
	$(KUBECTL) -n $(HELM_KCP_NS) rollout restart deployment/kcp-front-proxy
	$(KUBECTL) -n $(HELM_KCP_NS) rollout status deployment/kcp-front-proxy --timeout=600s

.PHONY: get-kcp-kubeconfig
get-kcp-kubeconfig: ## Build a self-contained kcp admin kubeconfig at KCP_KUBECONFIG
	@CA=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-ca -o jsonpath='{.data.tls\.crt}'); \
	 CERT=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert -o jsonpath='{.data.tls\.crt}'); \
	 KEY=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert -o jsonpath='{.data.tls\.key}'); \
	 printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: kcp\n  cluster:\n    server: https://localhost:6443/clusters/root\n    certificate-authority-data: %s\nusers:\n- name: kcp-admin\n  user:\n    client-certificate-data: %s\n    client-key-data: %s\ncontexts:\n- name: kcp\n  context:\n    cluster: kcp\n    user: kcp-admin\ncurrent-context: kcp\n' \
	   "$$CA" "$$CERT" "$$KEY" > $(KCP_KUBECONFIG)
	@echo "✓ kcp admin kubeconfig written to $(KCP_KUBECONFIG)"

.PHONY: bootstrap-kcp-workspaces
bootstrap-kcp-workspaces: ## Bootstrap the DBaaS provider and consumer workspaces in kcp
	$(KUBECTL) -n $(HELM_KCP_NS) create configmap kcp-bootstrap-scripts \
	  --from-file=workspaces.yaml=deploy/kcp/workspaces.yaml \
	  --from-file=apiexport.yaml=deploy/kcp/apiexport.yaml \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) -n $(HELM_KCP_NS) delete job kcp-bootstrap --ignore-not-found
	$(KUBECTL) apply -f deploy/kcp/bootstrap-job.yaml
	$(KUBECTL) -n $(HELM_KCP_NS) wait --for=condition=complete \
	  job/kcp-bootstrap --timeout=600s
	$(KUBECTL) -n $(HELM_KCP_NS) delete job kcp-bootstrap --ignore-not-found
	@echo "✓ kcp workspaces bootstrapped"
