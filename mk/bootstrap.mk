.PHONY: kind-create kind-delete
kind-create: check-capd-host ## Create the local kind management cluster
	$(KIND) create cluster --name $(KIND_CLUSTER_NAME) --config deploy/kind/kind-config.yaml
	@echo "✓ kind cluster '$(KIND_CLUSTER_NAME)' created"
	@echo "  kcp front-proxy → https://localhost:6443"
	@echo "  DBaaS provisioner UI → http://localhost:8090"

kind-delete: ## Delete the local kind management cluster
	$(KIND) delete cluster --name $(KIND_CLUSTER_NAME)

.PHONY: helm-repos
helm-repos: ## Add and update Helm repositories used by the deploy flow
	$(HELM) repo add kcp          https://kcp-dev.github.io/helm-charts        2>/dev/null || true
	$(HELM) repo add cert-manager https://charts.jetstack.io                   2>/dev/null || true
	$(HELM) repo add headlamp     https://kubernetes-sigs.github.io/headlamp/  2>/dev/null || true
	$(HELM) repo update

.PHONY: deploy-cert-manager undeploy-cert-manager
deploy-cert-manager: ## Install cert-manager into the local cluster
	$(HELM) upgrade --install cert-manager cert-manager/cert-manager \
	  -n cert-manager --create-namespace \
	  --set crds.enabled=true
	$(KUBECTL) -n cert-manager rollout status deployment/cert-manager --timeout=120s
	$(KUBECTL) -n cert-manager rollout status deployment/cert-manager-webhook --timeout=120s

undeploy-cert-manager: ## Remove cert-manager from the local cluster
	$(HELM) uninstall cert-manager -n cert-manager || true
