# DBaaS sample provider Makefile
# Ref: dbaas.md for full topology description.

SHELL := /bin/bash

# ── Upstream CRD source paths ──────────────────────────────────────────────────
MCK_SRC   ?= /Users/s.urbaniak/src/mongodb-kubernetes/config/crd/bases
ATLAS_SRC ?= /Users/s.urbaniak/src/mongodb-atlas-kubernetes/config/generated/crd/bases

# ── KCP Helm ────────────────────────────────────────────────────────────────────
HELM_KCP_CHART  := kcp/kcp
HELM_KCP_NS     := kcp
HELM_KCP_VALUES := deploy/kcp/kcp-values.yaml

# ── KCP admin kubeconfig (used by bootstrap-kcp-workspaces) ─────────────────────
KCP_KUBECONFIG ?= /tmp/kcp-admin.kubeconfig

# ── kro Helm ────────────────────────────────────────────────────────────────────
HELM_KRO_CHART  := kro/kro
HELM_KRO_NS     := kro
HELM_KRO_VALUES := deploy/kro/kro-values.yaml

# ── API Sync Agent ───────────────────────────────────────────────────────────────
HELM_AGENT_CHART := kcp/api-syncagent
HELM_AGENT_NS    := kcp

# ── ko image registry ────────────────────────────────────────────────────────────
# For kind use "kind.local"; for a local registry use e.g. "localhost:5001".
KO_DOCKER_REPO ?= kind.local

KUBECTL ?= kubectl
HELM    ?= helm
KO      ?= ko

.PHONY: all
all: refresh-crds

# ── CRD refresh ───────────────────────────────────────────────────────────────
.PHONY: refresh-crds refresh-mck-crds refresh-atlas-crds
refresh-crds: refresh-mck-crds refresh-atlas-crds

refresh-mck-crds:
	mkdir -p config/mck-crds
	cp $(MCK_SRC)/mongodb.com_mongodb.yaml             config/mck-crds/
	cp $(MCK_SRC)/mongodb.com_mongodbusers.yaml        config/mck-crds/
	cp $(MCK_SRC)/mongodb.com_clustermongodbroles.yaml config/mck-crds/
	@echo "✓ MCK CRDs refreshed from $(MCK_SRC)"

refresh-atlas-crds:
	mkdir -p config/atlas-crds
	cp $(ATLAS_SRC)/flexclusters.atlas.generated.mongodb.com.yaml config/atlas-crds/
	@echo "✓ Atlas CRDs refreshed from $(ATLAS_SRC)"

# ── Apply CRDs to physical cluster ─────────────────────────────────────────────
.PHONY: apply-mck-crds apply-atlas-crds apply-crds
apply-mck-crds:
	$(KUBECTL) apply -f config/mck-crds/

apply-atlas-crds:
	$(KUBECTL) apply -f config/atlas-crds/

apply-crds: apply-mck-crds apply-atlas-crds

# ── Helm repo bootstrap ────────────────────────────────────────────────────────
.PHONY: helm-repos
helm-repos:
	$(HELM) repo add kcp https://kcp-dev.github.io/helm-charts 2>/dev/null || true
	$(HELM) repo add kro https://kro.run/charts                2>/dev/null || true
	$(HELM) repo update

# ── KCP ───────────────────────────────────────────────────────────────────────
.PHONY: deploy-kcp undeploy-kcp
deploy-kcp: helm-repos
	$(HELM) upgrade --install kcp $(HELM_KCP_CHART) \
	  -n $(HELM_KCP_NS) --create-namespace \
	  -f $(HELM_KCP_VALUES)

undeploy-kcp:
	$(HELM) uninstall kcp -n $(HELM_KCP_NS) || true

# Extract the KCP admin kubeconfig into /tmp/kcp-admin.kubeconfig.
# Adjust the secret name/namespace to match your KCP Helm chart output.
.PHONY: get-kcp-kubeconfig
get-kcp-kubeconfig:
	$(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-kubeconfig \
	  -o jsonpath='{.data.kubeconfig}' | base64 -d > $(KCP_KUBECONFIG)
	@echo "✓ KCP admin kubeconfig written to $(KCP_KUBECONFIG)"

# Bootstrap the root:dbaas-provider and root:consumers workspaces in KCP.
# Must run AFTER deploy-kcp and get-kcp-kubeconfig.
.PHONY: bootstrap-kcp-workspaces
bootstrap-kcp-workspaces:
	KUBECONFIG=$(KCP_KUBECONFIG) $(KUBECTL) apply -f deploy/kcp/workspaces.yaml

# ── kro ───────────────────────────────────────────────────────────────────────
# IMPORTANT: deploy-kro must succeed before deploy-sync-agent.
# kro creates the MongoDBDatabase CRD dynamically; the sync agent must find it.
.PHONY: deploy-kro undeploy-kro
deploy-kro: helm-repos apply-crds
	$(HELM) upgrade --install kro $(HELM_KRO_CHART) \
	  -n $(HELM_KRO_NS) --create-namespace \
	  -f $(HELM_KRO_VALUES)
	@echo "Waiting for kro to be ready..."
	$(KUBECTL) -n $(HELM_KRO_NS) rollout status deployment/kro --timeout=120s
	$(KUBECTL) apply -f config/kro/mongodatabase-rgd.yaml
	@echo "✓ kro deployed and ResourceGraphDefinition applied"

undeploy-kro:
	$(KUBECTL) delete -f config/kro/mongodatabase-rgd.yaml --ignore-not-found
	$(HELM) uninstall kro -n $(HELM_KRO_NS) || true

# ── API Sync Agent ─────────────────────────────────────────────────────────────
.PHONY: deploy-sync-agent undeploy-sync-agent
deploy-sync-agent: helm-repos
	$(HELM) upgrade --install api-syncagent $(HELM_AGENT_CHART) \
	  -n $(HELM_AGENT_NS) --create-namespace
	$(KUBECTL) apply -f config/sync-agent/

undeploy-sync-agent:
	$(KUBECTL) delete -f config/sync-agent/ --ignore-not-found
	$(HELM) uninstall api-syncagent -n $(HELM_AGENT_NS) || true

# ── Mock controllers + provisioner ─────────────────────────────────────────────
.PHONY: ko-build ko-apply
ko-build:
	KO_DOCKER_REPO=$(KO_DOCKER_REPO) $(KO) build \
	  ./cmd/mock-mongodb/ \
	  ./cmd/mock-flexcluster/ \
	  ./cmd/provisioner/

ko-apply:
	KO_DOCKER_REPO=$(KO_DOCKER_REPO) $(KO) apply \
	  -f deploy/mock-mongodb/ \
	  -f deploy/mock-flexcluster/ \
	  -f deploy/provisioner/

# ── Full deploy sequence ────────────────────────────────────────────────────────
# Order matters: kro before sync-agent (kro creates the CRD the sync agent needs).
.PHONY: deploy
deploy: deploy-kcp apply-crds deploy-kro deploy-sync-agent ko-apply
	@echo ""
	@echo "═══════════════════════════════════════════════════════"
	@echo "  DBaaS stack deployed."
	@echo ""
	@echo "  Next steps:"
	@echo "  1. make get-kcp-kubeconfig"
	@echo "  2. make bootstrap-kcp-workspaces"
	@echo "  3. kubectl create secret generic kcp-admin-kubeconfig \\"
	@echo "       --from-file=kubeconfig=$(KCP_KUBECONFIG)"
	@echo "  4. kubectl rollout restart deployment/dbaas-provisioner"
	@echo "  5. kubectl port-forward svc/dbaas-provisioner 8090"
	@echo "     → open http://localhost:8090"
	@echo "═══════════════════════════════════════════════════════"

.PHONY: undeploy
undeploy: undeploy-sync-agent undeploy-kro undeploy-kcp
	$(KUBECTL) delete -f deploy/mock-mongodb/    --ignore-not-found
	$(KUBECTL) delete -f deploy/mock-flexcluster/ --ignore-not-found
	$(KUBECTL) delete -f deploy/provisioner/      --ignore-not-found
	$(KUBECTL) delete -f config/atlas-crds/       --ignore-not-found
	$(KUBECTL) delete -f config/mck-crds/         --ignore-not-found

# ── Local dev ──────────────────────────────────────────────────────────────────
.PHONY: build vet
build:
	go build ./...

vet:
	go vet ./...
