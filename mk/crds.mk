.PHONY: refresh-crds refresh-mck-crds refresh-atlas-crds
refresh-crds: refresh-mck-crds refresh-atlas-crds ## Refresh vendored upstream CRDs

refresh-mck-crds: ## Refresh MongoDB Community Operator CRDs from MCK_SRC
	mkdir -p config/mck-crds
	cp $(MCK_SRC)/mongodb.com_mongodb.yaml             config/mck-crds/
	cp $(MCK_SRC)/mongodb.com_mongodbusers.yaml        config/mck-crds/
	cp $(MCK_SRC)/mongodb.com_clustermongodbroles.yaml config/mck-crds/
	@echo "✓ MCK CRDs refreshed from $(MCK_SRC)"

refresh-atlas-crds: ## Refresh Atlas operator generated CRDs from the pinned AKO commit
	mkdir -p config/atlas-crds
	$(CURL) -fsSL https://raw.githubusercontent.com/mongodb/mongodb-atlas-kubernetes/$(AKO_COMMIT)/internal/generated/crds/crds.yaml \
	  -o config/atlas-crds/generated-crds.yaml
	@echo "✓ Atlas CRDs refreshed from AKO commit $(AKO_COMMIT)"

.PHONY: apply-mck-crds apply-atlas-crds apply-crds
apply-mck-crds: ## Apply MongoDB Community Operator CRDs to the cluster
	$(KUBECTL) apply -f config/mck-crds/

apply-atlas-crds: ## Apply Atlas operator CRDs to the cluster
	$(KUBECTL) apply -f config/atlas-crds/

apply-crds: apply-mck-crds apply-atlas-crds ## Apply all vendored CRDs
