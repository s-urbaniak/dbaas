HELM_KRO_CHART := oci://registry.k8s.io/kro/charts/kro
HELM_KRO_NS := kro
HELM_KRO_VALUES := deploy/kro/kro-values.yaml

.PHONY: deploy-kro undeploy-kro
deploy-kro: ## Install kro and the tenant ResourceGraphDefinitions
	$(HELM) upgrade --install kro $(HELM_KRO_CHART) \
	  -n $(HELM_KRO_NS) --create-namespace \
	  -f $(HELM_KRO_VALUES)
	@echo "Waiting for kro to be ready..."
	$(KUBECTL) -n $(HELM_KRO_NS) rollout status deployment/kro --timeout=600s
	$(KUBECTL) apply -f config/kro/mongodatabase-rgd.yaml
	$(KUBECTL) apply -f config/kro/kubernetes-rgd.yaml
	@echo "Waiting for kro to generate kubernetes.kro.run..."
	@until $(KUBECTL) get crd kubernetes.kro.run >/dev/null 2>&1; do sleep 2; done
	$(KUBECTL) patch crd kubernetes.kro.run --type json --patch-file deploy/kro/kubernetes-crd-patch.yaml
	@echo "✓ kro deployed and ResourceGraphDefinitions applied"

undeploy-kro: ## Remove kro and the tenant ResourceGraphDefinitions
	$(KUBECTL) delete -f config/kro/mongodatabase-rgd.yaml --ignore-not-found
	$(KUBECTL) delete -f config/kro/kubernetes-rgd.yaml --ignore-not-found
	$(HELM) uninstall kro -n $(HELM_KRO_NS) || true
