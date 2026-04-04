HELM_HEADLAMP_CHART := headlamp/headlamp
HELM_HEADLAMP_NS := headlamp
HELM_HEADLAMP_VALUES := deploy/headlamp/values.yaml
HEADLAMP_PLUGIN_DIR := headlamp-plugin/kcp

.PHONY: build-headlamp-plugin deploy-headlamp-plugin bootstrap-headlamp-kubeconfig deploy-headlamp undeploy-headlamp
build-headlamp-plugin: ## Build the Headlamp KCP plugin bundle
	cd $(HEADLAMP_PLUGIN_DIR) && npm ci && npx headlamp-plugin build

deploy-headlamp-plugin: build-headlamp-plugin ## Publish the Headlamp plugin ConfigMap
	$(KUBECTL) create namespace $(HELM_HEADLAMP_NS) \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) create configmap headlamp-kcp-plugin \
	  -n $(HELM_HEADLAMP_NS) \
	  --from-file=main.js=$(HEADLAMP_PLUGIN_DIR)/dist/main.js \
	  '--from-literal=package.json={"name":"kcp","version":"0.1.1","devDependencies":{"@kinvolk/headlamp-plugin":"^0.13.1"}}' \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -

bootstrap-headlamp-kubeconfig: ## Refresh the shared Headlamp kubeconfig secret data
	python3 $(SCRIPTS_DIR)/bootstrap_headlamp_kubeconfig.py

deploy-headlamp: deploy-headlamp-plugin ## Install Headlamp and its KCP plugin
	$(KUBECTL) apply -f deploy/headlamp/kubeconfig-secret.yaml
	$(KUBECTL) apply -f deploy/headlamp/rbac.yaml
	$(HELM) upgrade --install headlamp $(HELM_HEADLAMP_CHART) \
	  -n $(HELM_HEADLAMP_NS) --create-namespace \
	  -f $(HELM_HEADLAMP_VALUES)
	python3 $(SCRIPTS_DIR)/bootstrap_headlamp_kubeconfig.py
	$(KUBECTL) -n $(HELM_HEADLAMP_NS) rollout status deployment/headlamp --timeout=120s
	@echo "✓ Headlamp deployed → http://localhost:4466"

undeploy-headlamp: ## Remove Headlamp and its plugin resources
	$(HELM) uninstall headlamp -n $(HELM_HEADLAMP_NS) || true
	$(KUBECTL) delete configmap headlamp-kcp-plugin -n $(HELM_HEADLAMP_NS) --ignore-not-found
	$(KUBECTL) delete -f deploy/headlamp/rbac.yaml --ignore-not-found
	$(KUBECTL) delete -f deploy/headlamp/kubeconfig-secret.yaml --ignore-not-found
