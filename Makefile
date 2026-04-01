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

# Target platform for ko builds. Defaults to the host architecture so images
# load correctly into a local kind cluster (arm64 on Apple Silicon, amd64 elsewhere).
KO_PLATFORMS ?= linux/$(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

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
deploy-cert-manager:
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
	@echo "Waiting for KCP pods to be ready..."
	$(KUBECTL) -n $(HELM_KCP_NS) rollout status deployment/kcp            --timeout=600s
	$(KUBECTL) -n $(HELM_KCP_NS) rollout status deployment/kcp-front-proxy --timeout=600s
	$(KUBECTL) apply -f deploy/kcp/admin-cert.yaml
	# The KCP workspace controller uses kcp-external-admin-kubeconfig-cert (signed by
	# kcp-front-proxy-client-ca) to call back into kcp:6443/services/initializingworkspaces.
	# kcp:6443 only trusts kcp-client-ca, causing Unauthorized errors and workspaces
	# staying in Initializing forever. Fix: combine both CAs into one bundle and patch
	# the deployment to use it as --client-ca-file.
	$(MAKE) patch-kcp-client-ca
	@echo "Waiting for kcp-admin-client-cert to be issued by cert-manager..."
	@until $(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert >/dev/null 2>&1; do sleep 2; done
	@echo "✓ kcp-admin-client-cert ready"

# Combine kcp-client-ca + kcp-front-proxy-client-ca into a single bundle so the
# KCP API server trusts both. Must run after cert-manager has issued both CA secrets.
.PHONY: patch-kcp-client-ca
patch-kcp-client-ca:
	@echo "Waiting for kcp-client-ca and kcp-front-proxy-client-ca secrets..."
	@until $(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-client-ca kcp-front-proxy-client-ca >/dev/null 2>&1; do sleep 2; done
	@CLIENT_CA=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-client-ca -o jsonpath='{.data.tls\.crt}' | base64 -d); \
	 FP_CA=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-front-proxy-client-ca -o jsonpath='{.data.tls\.crt}' | base64 -d); \
	 $(KUBECTL) -n $(HELM_KCP_NS) create secret generic kcp-combined-client-ca \
	   --from-literal="tls.crt=$${CLIENT_CA}"$$'\n'"$${FP_CA}" \
	   --dry-run=client -o yaml | $(KUBECTL) apply -f -
	@IDX=$$($(KUBECTL) -n $(HELM_KCP_NS) get deployment kcp -o json | \
	  python3 -c "import sys,json; vols=json.load(sys.stdin)['spec']['template']['spec']['volumes']; \
	  [print(i) for i,v in enumerate(vols) if v.get('name')=='kcp-client-ca']"); \
	 $(KUBECTL) -n $(HELM_KCP_NS) patch deployment kcp --type=json \
	   -p="[{\"op\":\"replace\",\"path\":\"/spec/template/spec/volumes/$${IDX}/secret/secretName\",\"value\":\"kcp-combined-client-ca\"}]"
	@echo "✓ KCP deployment patched to trust kcp-front-proxy-client-ca for client auth"

undeploy-kcp:
	$(HELM) uninstall kcp -n $(HELM_KCP_NS) || true

# Extract the KCP admin kubeconfig into /tmp/kcp-admin.kubeconfig.
# Adjust the secret name/namespace to match your KCP Helm chart output.
.PHONY: get-kcp-kubeconfig
get-kcp-kubeconfig:
	# The kubeconfig stored in the secret references cert files at /etc/kcp/...
	# (in-pod paths inaccessible from the host). Build a self-contained kubeconfig
	# by embedding the cert data from kcp-external-admin-kubeconfig-cert inline.
	@CA=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-ca \
	         -o jsonpath='{.data.tls\.crt}'); \
	 CERT=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert \
	           -o jsonpath='{.data.tls\.crt}'); \
	 KEY=$$($(KUBECTL) -n $(HELM_KCP_NS) get secret kcp-admin-client-cert \
	          -o jsonpath='{.data.tls\.key}'); \
	 printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: kcp\n  cluster:\n    server: https://localhost:6443/clusters/root\n    certificate-authority-data: %s\nusers:\n- name: kcp-admin\n  user:\n    client-certificate-data: %s\n    client-key-data: %s\ncontexts:\n- name: kcp\n  context:\n    cluster: kcp\n    user: kcp-admin\ncurrent-context: kcp\n' \
	   "$$CA" "$$CERT" "$$KEY" > $(KCP_KUBECONFIG)
	@echo "✓ KCP admin kubeconfig written to $(KCP_KUBECONFIG)"

# Port-forward the KCP front-proxy to localhost:6443.
# Run this in a separate terminal after deploy-kcp.
.PHONY: kcp-port-forward
kcp-port-forward:
	$(KUBECTL) -n $(HELM_KCP_NS) port-forward svc/kcp-front-proxy 6443:8443

# Bootstrap the root:dbaas-provider and root:consumers workspaces in KCP.
# Runs as a Kubernetes Job inside the cluster — no local port-forward required.
# Must run AFTER deploy-kcp (kcp-admin-client-cert secret must exist).
.PHONY: bootstrap-kcp-workspaces
bootstrap-kcp-workspaces:
	# Build ConfigMap from the workspace and APIExport manifests.
	$(KUBECTL) -n $(HELM_KCP_NS) create configmap kcp-bootstrap-scripts \
	  --from-file=workspaces.yaml=deploy/kcp/workspaces.yaml \
	  --from-file=apiexport.yaml=deploy/kcp/apiexport.yaml \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -
	# Delete any previous run before creating a fresh Job.
	$(KUBECTL) -n $(HELM_KCP_NS) delete job kcp-bootstrap --ignore-not-found
	$(KUBECTL) apply -f deploy/kcp/bootstrap-job.yaml
	$(KUBECTL) -n $(HELM_KCP_NS) wait --for=condition=complete \
	  job/kcp-bootstrap --timeout=600s
	$(KUBECTL) -n $(HELM_KCP_NS) delete job kcp-bootstrap --ignore-not-found
	@echo "✓ KCP workspaces bootstrapped"

# ── kro ───────────────────────────────────────────────────────────────────────
# IMPORTANT: deploy-kro must succeed before deploy-sync-agent.
# kro creates the MongoDBDatabase CRD dynamically; the sync agent must find it.
.PHONY: deploy-kro undeploy-kro
deploy-kro: apply-crds
	$(HELM) upgrade --install kro $(HELM_KRO_CHART) \
	  -n $(HELM_KRO_NS) --create-namespace \
	  -f $(HELM_KRO_VALUES)
	@echo "Waiting for kro to be ready..."
	$(KUBECTL) -n $(HELM_KRO_NS) rollout status deployment/kro --timeout=600s
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
	# Repoint to in-cluster KCP service. TLS verification works because
	# kcp-values.yaml adds kcp-front-proxy.kcp.svc.cluster.local to extraDNSNames.
	KUBECONFIG=/tmp/kcp-syncagent.kubeconfig $(KUBECTL) config set-cluster \
	  $$(KUBECONFIG=/tmp/kcp-syncagent.kubeconfig $(KUBECTL) config view -o jsonpath='{.clusters[0].name}') \
	  --server=https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/root:dbaas-provider
	$(KUBECTL) -n $(HELM_AGENT_NS) create secret generic kcp-syncagent-kubeconfig \
	  --from-file=kubeconfig=/tmp/kcp-syncagent.kubeconfig \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -
	@rm -f /tmp/kcp-syncagent.kubeconfig
	@echo "✓ kcp-syncagent-kubeconfig secret created in namespace $(HELM_AGENT_NS)"

# Create the provisioner's KCP kubeconfig secret inside the cluster.
# Same as create-sync-agent-secret but scoped to root:consumers (the provisioner
# lists and creates workspaces there) with in-cluster server URL.
.PHONY: create-provisioner-secret
create-provisioner-secret:
	cp $(KCP_KUBECONFIG) /tmp/kcp-provisioner.kubeconfig
	KUBECONFIG=/tmp/kcp-provisioner.kubeconfig $(KUBECTL) config set-cluster \
	  $$(KUBECONFIG=/tmp/kcp-provisioner.kubeconfig $(KUBECTL) config view -o jsonpath='{.clusters[0].name}') \
	  --server=https://kcp-front-proxy.kcp.svc.cluster.local:8443/clusters/root
	$(KUBECTL) create secret generic kcp-admin-kubeconfig \
	  --from-file=kubeconfig=/tmp/kcp-provisioner.kubeconfig \
	  --dry-run=client -o yaml | $(KUBECTL) apply -f -
	@rm -f /tmp/kcp-provisioner.kubeconfig
	@echo "✓ kcp-admin-kubeconfig secret created in default namespace"

.PHONY: deploy-sync-agent undeploy-sync-agent
deploy-sync-agent: create-sync-agent-secret
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
	KO_DOCKER_REPO=$(KO_DOCKER_REPO) $(KO) build --platform=$(KO_PLATFORMS) \
	  ./cmd/mock-mongodb/ \
	  ./cmd/mock-flexcluster/ \
	  ./cmd/provisioner/

ko-apply:
	KO_DOCKER_REPO=$(KO_DOCKER_REPO) $(KO) apply --platform=$(KO_PLATFORMS) \
	  -f deploy/mock-mongodb/ \
	  -f deploy/mock-flexcluster/ \
	  -f deploy/provisioner/

# ── Full deploy sequence ────────────────────────────────────────────────────────
# Single target — no port-forward required. The pipeline script renders a status
# bar at the bottom of the terminal with spinner animation.
#
# Individual phase targets (deploy-phase1, deploy-phase2) are kept for manual
# step-by-step use. kcp-port-forward is available for debugging.
.PHONY: deploy-phase1
deploy-phase1: deploy-kcp apply-crds deploy-kro
	@echo ""
	@echo "  Phase 1 complete (cert-manager, KCP, CRDs, kro)."
	@echo "  Continue with: make deploy-phase2"
	@echo "  (No port-forward needed — bootstrap runs as an in-cluster Job)"

.PHONY: deploy-phase2
deploy-phase2: get-kcp-kubeconfig bootstrap-kcp-workspaces deploy-sync-agent create-provisioner-secret ko-apply
	@echo ""
	@echo "  Phase 2 complete. Expose the provisioner:"
	@echo "    kubectl port-forward svc/dbaas-provisioner 8090"
	@echo "    → open http://localhost:8090"

.PHONY: deploy
deploy:
	@python3 scripts/deploy.py \
	  --step "helm-repos"   "$(MAKE) helm-repos" \
	  --step "cert-manager" "$(MAKE) deploy-cert-manager" \
	  --step "kcp"          "$(MAKE) deploy-kcp" \
	  --step "crds"         "$(MAKE) apply-crds" \
	  --step "kro"          "$(MAKE) deploy-kro" \
	  --step "kubeconfig"   "$(MAKE) get-kcp-kubeconfig" \
	  --step "bootstrap"    "$(MAKE) bootstrap-kcp-workspaces" \
	  --step "sync-agent"   "$(MAKE) deploy-sync-agent" \
	  --step "provisioner"  "$(MAKE) create-provisioner-secret" \
	  --step "controllers"  "$(MAKE) ko-apply"

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
