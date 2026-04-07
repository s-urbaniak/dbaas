HELM_AGENT_CHART := kcp/api-syncagent
HELM_AGENT_NS := kcp
HELM_KUBERNETES_AGENT_NS := kcp-kubernetes

.PHONY: create-sync-agent-secret
create-sync-agent-secret: | $(BUILD_DIR) ## Create the in-cluster kcp kubeconfig secret for the API sync agent
	$(KUBECTL) create namespace $(HELM_AGENT_NS) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	bash $(SCRIPTS_DIR)/create_kubeconfig_secret.sh \
	  "$(KUBECTL)" \
	  "$(KCP_KUBECONFIG)" \
	  $(BUILD_DIR)/kcp-syncagent.kubeconfig \
	  https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/root:dbaas-provider \
	  "$(HELM_AGENT_NS)" \
	  kcp-syncagent-kubeconfig \
	  kubeconfig
	@echo "✓ kcp-syncagent-kubeconfig secret created in namespace $(HELM_AGENT_NS)"

.PHONY: create-kubernetes-sync-agent-secret
create-kubernetes-sync-agent-secret: | $(BUILD_DIR) ## Create the in-cluster kcp kubeconfig secret for the Kubernetes API sync agent
	$(KUBECTL) create namespace $(HELM_KUBERNETES_AGENT_NS) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	bash $(SCRIPTS_DIR)/create_kubeconfig_secret.sh \
	  "$(KUBECTL)" \
	  "$(KCP_KUBECONFIG)" \
	  $(BUILD_DIR)/kcp-kubernetes-syncagent.kubeconfig \
	  https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/root:dbaas-provider \
	  "$(HELM_KUBERNETES_AGENT_NS)" \
	  kcp-syncagent-kubeconfig \
	  kubeconfig
	@echo "✓ kcp-syncagent-kubeconfig secret created in namespace $(HELM_KUBERNETES_AGENT_NS)"

.PHONY: create-provisioner-secret
create-provisioner-secret: | $(BUILD_DIR) ## Create the in-cluster kcp kubeconfig secret for the provisioner
	bash $(SCRIPTS_DIR)/create_kubeconfig_secret.sh \
	  "$(KUBECTL)" \
	  "$(KCP_KUBECONFIG)" \
	  $(BUILD_DIR)/kcp-provisioner.kubeconfig \
	  https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/root \
	  default \
	  kcp-admin-kubeconfig \
	  kubeconfig
	@echo "✓ kcp-admin-kubeconfig secret created in default namespace"

.PHONY: deploy-sync-agent undeploy-sync-agent
deploy-sync-agent: create-sync-agent-secret create-kubernetes-sync-agent-secret ## Install the kcp API sync agents and RBAC
	$(HELM) upgrade --install api-syncagent-mongodb $(HELM_AGENT_CHART) \
	  -n $(HELM_AGENT_NS) --create-namespace \
	  -f deploy/sync-agent/values.yaml
	$(HELM) upgrade --install api-syncagent-kubernetes $(HELM_AGENT_CHART) \
	  -n $(HELM_KUBERNETES_AGENT_NS) --create-namespace \
	  -f deploy/sync-agent/kubernetes-values.yaml
	$(KUBECTL) apply -f deploy/sync-agent/rbac.yaml
	$(KUBECTL) apply -f config/sync-agent/

undeploy-sync-agent: ## Remove the kcp API sync agent and synced API config
	$(KUBECTL) delete -f config/sync-agent/ --ignore-not-found
	$(KUBECTL) delete -f deploy/sync-agent/rbac.yaml --ignore-not-found
	$(HELM) uninstall api-syncagent-mongodb -n $(HELM_AGENT_NS) || true
	$(HELM) uninstall api-syncagent-kubernetes -n $(HELM_KUBERNETES_AGENT_NS) || true
