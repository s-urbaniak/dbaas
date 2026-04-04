# Common tooling and help targets shared across the DBaaS Make fragments.

ROOT_DIR := $(abspath .)
SCRIPTS_DIR := $(ROOT_DIR)/scripts

MCK_SRC   ?= /Users/s.urbaniak/src/mongodb-kubernetes/config/crd/bases
ATLAS_SRC ?= /Users/s.urbaniak/src/mongodb-atlas-kubernetes/config/generated/crd/bases

KCP_KUBECONFIG ?= /tmp/kcp-admin.kubeconfig

KO_DOCKER_REPO ?= kind.local
KIND_CLUSTER_NAME ?= dbaas
KO_PLATFORMS ?= linux/$(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

KUBECTL ?= kubectl
HELM ?= helm
KO ?= ko
KIND ?= kind
CLUSTERCTL ?= clusterctl

.PHONY: require-%
require-%:
	@command -v $* >/dev/null 2>&1 || { echo "$* is required but not installed."; exit 1; }

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_.-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
