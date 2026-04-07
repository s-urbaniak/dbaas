.PHONY: ako-fetch create-ako-secret deploy-ako undeploy-ako

AKO_IMAGE_REPO ?= kind.local/mongodb-atlas-kubernetes-operator

ako-fetch: require-git | $(BUILD_DIR) ## Fetch the pinned Atlas operator source tree
	rm -rf $(AKO_SRC_DIR)
	git clone https://github.com/mongodb/mongodb-atlas-kubernetes $(AKO_SRC_DIR)
	git -C $(AKO_SRC_DIR) checkout $(AKO_COMMIT)

create-ako-secret: ## Create the global Atlas credentials secret for AKO
	@test -n "$(ATLAS_ORG_ID)" || { echo "ATLAS_ORG_ID is required"; exit 1; }
	@test -n "$(ATLAS_PUBLIC_API_KEY)" || { echo "ATLAS_PUBLIC_API_KEY is required"; exit 1; }
	@test -n "$(ATLAS_PRIVATE_API_KEY)" || { echo "ATLAS_PRIVATE_API_KEY is required"; exit 1; }
	$(KUBECTL) create namespace $(ATLAS_OPERATOR_NAMESPACE) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) -n $(ATLAS_OPERATOR_NAMESPACE) create secret generic $(ATLAS_OPERATOR_SECRET_NAME) \
	  --from-literal=orgId="$(ATLAS_ORG_ID)" \
	  --from-literal=publicApiKey="$(ATLAS_PUBLIC_API_KEY)" \
	  --from-literal=privateApiKey="$(ATLAS_PRIVATE_API_KEY)" \
	  --dry-run=client -o yaml | \
	  $(KUBECTL) label --local -f - atlas.mongodb.com/type=credentials -o yaml | \
	  $(KUBECTL) apply -f -

deploy-ako: refresh-atlas-crds ako-fetch create-ako-secret ## Build and deploy the real Atlas operator from the pinned upstream source
	AKO_IMAGE=$$(cd $(AKO_SRC_DIR) && KO_DOCKER_REPO=$(KO_DOCKER_REPO) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) $(KO) build --platform=$(KO_PLATFORMS) ./cmd) && \
	  $(KUBECTL) apply -f config/atlas-crds/generated-crds.yaml && \
	  $(KUBECTL) apply -f $(AKO_LEGACY_CRDS_DIR) && \
	  sed "s#AKO_IMAGE_PLACEHOLDER#$$AKO_IMAGE#" deploy/atlas-operator/allinone.yaml | $(KUBECTL) apply -f - && \
	  $(KUBECTL) -n $(ATLAS_OPERATOR_NAMESPACE) rollout status deployment/mongodb-atlas-operator --timeout=600s

undeploy-ako: ## Remove the Atlas operator deployment and secret
	$(KUBECTL) delete -f deploy/atlas-operator/allinone.yaml --ignore-not-found
	$(KUBECTL) delete -f $(AKO_LEGACY_CRDS_DIR) --ignore-not-found
	$(KUBECTL) delete namespace $(ATLAS_OPERATOR_NAMESPACE) --ignore-not-found
