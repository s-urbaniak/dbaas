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
# kro is distributed as an OCI chart — no helm repo add needed.
HELM_KRO_CHART  := oci://registry.k8s.io/kro/charts/kro
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
	$(HELM) repo add kcp          https://kcp-dev.github.io/helm-charts 2>/dev/null || true
	$(HELM) repo add cert-manager https://charts.jetstack.io            2>/dev/null || true
	# kro is an OCI chart — no repo entry needed
	$(HELM) repo update

# ── cert-manager (required by KCP for TLS certificate management) ─────────────
.PHONY: deploy-cert-manager undeploy-cert-manager
deploy-cert-manager: helm-repos
	$(HELM) upgrade --install cert-manager cert-manager/cert-manager \
	  -n cert-manager --create-namespace \
	  --set crds.enabled=true
	$(KUBECTL) -n cert-manager rollout status deployment/cert-manager --timeout=120s
	$(KUBECTL) -n cert-manager rollout status deployment/cert-manager-webhook --timeout=120s

undeploy-cert-manager:
	$(HELM) uninstall cert-manager -n cert-manager || true

# ── KCP ───────────────────────────────────────────────────────────────────────
.PHONY: deploy-kcp undeploy-kcp
deploy-kcp: deploy-cert-manager
	$(HELM) upgrade --install kcp $(HELM_KCP_CHART) \
	  -n $(HELM_KCP_NS) --create-namespace \
	  -f $(HELM_KCP_VALUES)

undeploy-kcp:
	$(HELM) uninstall kcp -n $(HELM_KCP_NS) || true

# Extract the KCP admin kubeconfig into /tmp/kcp-admin.kubeconfig.
# Adjust the secret name/namespace to match your KCP Helm chart output.
.PHONY: get-kcp-kubeconfig
get-kcp-kubeconfig:
	$(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-external-admin-kubeconfig \
	  -o jsonpath='{.data.kubeconfig}' | base64 -d > $(KCP_KUBECONFIG)
	# Rewrite the server URL to localhost:6443 so it works with port-forward.
	KUBECONFIG=$(KCP_KUBECONFIG) $(KUBECTL) config set-cluster \
	  $$(KUBECONFIG=$(KCP_KUBECONFIG) $(KUBECTL) config view -o jsonpath='{.clusters[0].name}') \
	  --server=https://localhost:6443
	@echo "✓ KCP admin kubeconfig written to $(KCP_KUBECONFIG)"

# Port-forward the KCP front-proxy to localhost:6443.
# Run this in a separate terminal after deploy-kcp.
.PHONY: kcp-port-forward
kcp-port-forward:
	$(KUBECTL) -n $(HELM_KCP_NS) port-forward svc/kcp-front-proxy 6443:8443

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

# Create the sync agent's KCP kubeconfig secret inside the cluster.
# The admin kubeconfig (KCP_KUBECONFIG) uses localhost:6443 (port-forward) which
# is unreachable from pods. We rewrite the server URL to the in-cluster KCP
# front-proxy service address before storing it as a secret.
.PHONY: create-sync-agent-secret
create-sync-agent-secret:
	cp $(KCP_KUBECONFIG) /tmp/kcp-syncagent.kubeconfig
	KUBECONFIG=/tmp/kcp-syncagent.kubeconfig $(KUBECTL) config set-cluster \
	  $$(KUBECONFIG=/tmp/kcp-syncagent.kubeconfig $(KUBECTL) config view -o jsonpath='{.clusters[0].name}') \
	  --server=https://kcp-front-proxy.kcp.svc.cluster.local:8443
	$(KUBECTL) -n $(HELM_AGENT_NS) create secret generic kcp-syncagent-kubeconfig \
	  --from-file=kubeconfig=/tmp/kcp-syncagent.kubeconfig \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -
	@rm -f /tmp/kcp-syncagent.kubeconfig
	@echo "✓ kcp-syncagent-kubeconfig secret created in namespace $(HELM_AGENT_NS)"

.PHONY: deploy-sync-agent undeploy-sync-agent
deploy-sync-agent: helm-repos create-sync-agent-secret
	$(HELM) upgrade --install api-syncagent $(HELM_AGENT_CHART) \
	  -n $(HELM_AGENT_NS) --create-namespace \
	  -f deploy/sync-agent/values.yaml
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
# Deploying requires a port-forward to KCP mid-way through, so the full sequence
# is split into two phases:
#
#   Phase 1 (no port-forward needed):
#     make deploy-phase1
#       → cert-manager → KCP → CRDs → kro
#
#   Then in a separate terminal:
#     make kcp-port-forward
#
#   Phase 2 (port-forward must be running):
#     make deploy-phase2
#       → get-kcp-kubeconfig → bootstrap-kcp-workspaces → sync-agent → controllers
#
# deploy-phase1 and deploy-phase2 together replace the old one-shot `make deploy`.
.PHONY: deploy-phase1
deploy-phase1: deploy-kcp apply-crds deploy-kro
	@echo ""
	@echo "═══════════════════════════════════════════════════════"
	@echo "  Phase 1 complete (cert-manager, KCP, CRDs, kro)."
	@echo ""
	@echo "  Now run in a separate terminal and keep it running:"
	@echo "    make kcp-port-forward"
	@echo ""
	@echo "  Then continue with:"
	@echo "    make deploy-phase2"
	@echo "═══════════════════════════════════════════════════════"

.PHONY: deploy-phase2
deploy-phase2: get-kcp-kubeconfig bootstrap-kcp-workspaces deploy-sync-agent ko-apply
	@echo ""
	@echo "═══════════════════════════════════════════════════════"
	@echo "  Phase 2 complete (workspaces, sync-agent, controllers)."
	@echo ""
	@echo "  Expose the provisioner:"
	@echo "    kubectl create secret generic kcp-admin-kubeconfig \\"
	@echo "      --from-file=kubeconfig=$(KCP_KUBECONFIG)"
	@echo "    kubectl rollout restart deployment/dbaas-provisioner"
	@echo "    kubectl port-forward svc/dbaas-provisioner 8090"
	@echo "    → open http://localhost:8090"
	@echo "═══════════════════════════════════════════════════════"

.PHONY: deploy
deploy: deploy-phase1 deploy-phase2

.PHONY: undeploy
undeploy: undeploy-sync-agent undeploy-kro undeploy-kcp undeploy-cert-manager
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
