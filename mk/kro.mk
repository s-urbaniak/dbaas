HELM_KRO_CHART := oci://registry.k8s.io/kro/charts/kro
HELM_KRO_NS := kro
HELM_KRO_VALUES := deploy/kro/kro-values.yaml

.PHONY: deploy-kro undeploy-kro
deploy-kro: ## Install kro and the MongoDBDatabase ResourceGraphDefinition
	$(HELM) upgrade --install kro $(HELM_KRO_CHART) \
	  -n $(HELM_KRO_NS) --create-namespace \
	  -f $(HELM_KRO_VALUES)
	@echo "Waiting for kro to be ready..."
	$(KUBECTL) -n $(HELM_KRO_NS) rollout status deployment/kro --timeout=600s
	$(KUBECTL) apply -f config/kro/mongodatabase-rgd.yaml
	@echo "✓ kro deployed and ResourceGraphDefinition applied"

undeploy-kro: ## Remove kro and the MongoDBDatabase ResourceGraphDefinition
	$(KUBECTL) delete -f config/kro/mongodatabase-rgd.yaml --ignore-not-found
	$(HELM) uninstall kro -n $(HELM_KRO_NS) || true
