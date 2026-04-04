HELM_KCP_CHART := kcp/kcp
HELM_KCP_NS := kcp
HELM_KCP_VALUES := deploy/kcp/kcp-values.yaml

.PHONY: deploy-kcp undeploy-kcp
deploy-kcp: ## Install KCP and patch client CA handling for workspace init
	$(HELM) upgrade --install kcp $(HELM_KCP_CHART) \
	  -n $(HELM_KCP_NS) --create-namespace \
	  -f $(HELM_KCP_VALUES)
	@echo "Waiting for KCP pods to be ready..."
	$(KUBECTL) -n $(HELM_KCP_NS) rollout status deployment/kcp --timeout=600s
	$(KUBECTL) -n $(HELM_KCP_NS) rollout status deployment/kcp-front-proxy --timeout=600s
	$(KUBECTL) apply -f deploy/kcp/admin-cert.yaml
	$(MAKE) patch-kcp-client-ca
	@echo "Waiting for kcp-admin-client-cert to be issued by cert-manager..."
	@until $(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert >/dev/null 2>&1; do sleep 2; done
	@echo "✓ kcp-admin-client-cert ready"

.PHONY: patch-kcp-client-ca
patch-kcp-client-ca: ## Patch KCP to trust the front-proxy client CA for workspace init
	bash $(SCRIPTS_DIR)/patch_kcp_client_ca.sh "$(KUBECTL)" "$(HELM_KCP_NS)"

undeploy-kcp: ## Remove KCP from the local cluster
	$(HELM) uninstall kcp -n $(HELM_KCP_NS) || true

.PHONY: get-kcp-kubeconfig
get-kcp-kubeconfig: ## Build a self-contained KCP admin kubeconfig at KCP_KUBECONFIG
	@CA=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-ca -o jsonpath='{.data.tls\.crt}'); \
	 CERT=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert -o jsonpath='{.data.tls\.crt}'); \
	 KEY=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert -o jsonpath='{.data.tls\.key}'); \
	 printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: kcp\n  cluster:\n    server: https://localhost:6443/clusters/root\n    certificate-authority-data: %s\nusers:\n- name: kcp-admin\n  user:\n    client-certificate-data: %s\n    client-key-data: %s\ncontexts:\n- name: kcp\n  context:\n    cluster: kcp\n    user: kcp-admin\ncurrent-context: kcp\n' \
	   "$$CA" "$$CERT" "$$KEY" > $(KCP_KUBECONFIG)
	@echo "✓ KCP admin kubeconfig written to $(KCP_KUBECONFIG)"

.PHONY: bootstrap-kcp-workspaces
bootstrap-kcp-workspaces: ## Bootstrap the DBaaS provider and consumer workspaces in KCP
	$(KUBECTL) -n $(HELM_KCP_NS) create configmap kcp-bootstrap-scripts \
	  --from-file=workspaces.yaml=deploy/kcp/workspaces.yaml \
	  --from-file=apiexport.yaml=deploy/kcp/apiexport.yaml \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) -n $(HELM_KCP_NS) delete job kcp-bootstrap --ignore-not-found
	$(KUBECTL) apply -f deploy/kcp/bootstrap-job.yaml
	$(KUBECTL) -n $(HELM_KCP_NS) wait --for=condition=complete \
	  job/kcp-bootstrap --timeout=600s
	$(KUBECTL) -n $(HELM_KCP_NS) delete job kcp-bootstrap --ignore-not-found
	@echo "✓ KCP workspaces bootstrapped"
