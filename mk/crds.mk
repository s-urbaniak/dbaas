.PHONY: refresh-crds refresh-mck-crds refresh-atlas-crds
refresh-crds: refresh-mck-crds refresh-atlas-crds ## Refresh vendored upstream CRDs

refresh-mck-crds: ## Refresh MongoDB Community Operator CRDs from MCK_SRC
	mkdir -p config/mck-crds
	cp $(MCK_SRC)/mongodb.com_mongodb.yaml             config/mck-crds/
	cp $(MCK_SRC)/mongodb.com_mongodbusers.yaml        config/mck-crds/
	cp $(MCK_SRC)/mongodb.com_clustermongodbroles.yaml config/mck-crds/
	@echo "✓ MCK CRDs refreshed from $(MCK_SRC)"

refresh-atlas-crds: ## Refresh Atlas operator CRDs from ATLAS_SRC
	mkdir -p config/atlas-crds
	cp $(ATLAS_SRC)/flexclusters.atlas.generated.mongodb.com.yaml config/atlas-crds/
	@echo "✓ Atlas CRDs refreshed from $(ATLAS_SRC)"

.PHONY: apply-mck-crds apply-atlas-crds apply-crds
apply-mck-crds: ## Apply MongoDB Community Operator CRDs to the cluster
	$(KUBECTL) apply -f config/mck-crds/

apply-atlas-crds: ## Apply Atlas operator CRDs to the cluster
	$(KUBECTL) apply -f config/atlas-crds/

apply-crds: apply-mck-crds apply-atlas-crds ## Apply all vendored CRDs
