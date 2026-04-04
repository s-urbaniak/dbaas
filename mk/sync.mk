HELM_AGENT_CHART := kcp/api-syncagent
HELM_AGENT_NS := kcp

.PHONY: create-sync-agent-secret
create-sync-agent-secret: ## Create the in-cluster KCP kubeconfig secret for the API sync agent
	bash $(SCRIPTS_DIR)/create_kubeconfig_secret.sh \
	  "$(KUBECTL)" \
	  "$(KCP_KUBECONFIG)" \
	  /tmp/kcp-syncagent.kubeconfig \
	  https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/root:dbaas-provider \
	  "$(HELM_AGENT_NS)" \
	  kcp-syncagent-kubeconfig \
	  kubeconfig
	@echo "✓ kcp-syncagent-kubeconfig secret created in namespace $(HELM_AGENT_NS)"

.PHONY: create-provisioner-secret
create-provisioner-secret: ## Create the in-cluster KCP kubeconfig secret for the provisioner
	bash $(SCRIPTS_DIR)/create_kubeconfig_secret.sh \
	  "$(KUBECTL)" \
	  "$(KCP_KUBECONFIG)" \
	  /tmp/kcp-provisioner.kubeconfig \
	  https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/root \
	  default \
	  kcp-admin-kubeconfig \
	  kubeconfig
	@echo "✓ kcp-admin-kubeconfig secret created in default namespace"

.PHONY: deploy-sync-agent undeploy-sync-agent
deploy-sync-agent: create-sync-agent-secret ## Install the KCP API sync agent and RBAC
	$(HELM) upgrade --install api-syncagent $(HELM_AGENT_CHART) \
	  -n $(HELM_AGENT_NS) --create-namespace \
	  -f deploy/sync-agent/values.yaml
	$(KUBECTL) apply -f deploy/sync-agent/rbac.yaml
	$(KUBECTL) apply -f config/sync-agent/

undeploy-sync-agent: ## Remove the KCP API sync agent and synced API config
	$(KUBECTL) delete -f config/sync-agent/ --ignore-not-found
	$(KUBECTL) delete -f deploy/sync-agent/rbac.yaml --ignore-not-found
	$(HELM) uninstall api-syncagent -n $(HELM_AGENT_NS) || true
