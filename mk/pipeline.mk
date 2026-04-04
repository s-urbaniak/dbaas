.PHONY: deploy-phase1
deploy-phase1: deploy-capi deploy-kcp apply-crds deploy-kro ## Run the first half of the local deploy
	@echo ""
	@echo "  Phase 1 complete (CAPI, kcp, CRDs, kro)."
	@echo "  Continue with: make deploy-phase2"

.PHONY: deploy-phase2
deploy-phase2: get-kcp-kubeconfig bootstrap-kcp-workspaces deploy-sync-agent create-provisioner-secret ko-apply ## Run the second half of the local deploy
	@echo ""
	@echo "  Phase 2 complete."
	@echo "  → open http://localhost:8090"

.PHONY: deploy
deploy: ## Run the full local deploy pipeline
	@python3 $(SCRIPTS_DIR)/deploy.py \
	  --step "kind"         "$(MAKE) kind-create" \
	  --step "helm-repos"   "$(MAKE) helm-repos" \
	  --step "cert-manager" "$(MAKE) deploy-cert-manager" \
	  --step "capi"         "$(MAKE) deploy-capi" \
	  --step "kcp"          "$(MAKE) deploy-kcp" \
	  --step "crds"         "$(MAKE) apply-crds" \
	  --step "kro"          "$(MAKE) deploy-kro" \
	  --step "kubeconfig"   "$(MAKE) get-kcp-kubeconfig" \
	  --step "bootstrap"    "$(MAKE) bootstrap-kcp-workspaces" \
	  --step "sync-agent"   "$(MAKE) deploy-sync-agent" \
	  --step "provisioner"  "$(MAKE) create-provisioner-secret" \
	  --step "controllers"  "$(MAKE) ko-apply" \
	  --step "headlamp"     "$(MAKE) deploy-headlamp"

.PHONY: undeploy
undeploy: undeploy-sync-agent undeploy-headlamp undeploy-kro undeploy-kcp undeploy-capi undeploy-cert-manager ## Remove the deployed local stack from the current cluster
	$(KUBECTL) delete -f deploy/mock-mongodb/     --ignore-not-found
	$(KUBECTL) delete -f deploy/mock-flexcluster/ --ignore-not-found
	$(KUBECTL) delete -f deploy/kubernetes-controller/ --ignore-not-found
	$(KUBECTL) delete -f deploy/provisioner/      --ignore-not-found
	$(KUBECTL) delete -f config/atlas-crds/       --ignore-not-found
	$(KUBECTL) delete -f config/mck-crds/         --ignore-not-found
