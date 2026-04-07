# Common tooling and help targets shared across the DBaaS Make fragments.

ROOT_DIR  := $(abspath .)
BUILD_DIR := $(ROOT_DIR)/build
SCRIPTS_DIR := $(ROOT_DIR)/scripts

MCK_SRC   ?= /Users/s.urbaniak/src/mongodb-kubernetes/config/crd/bases
AKO_COMMIT ?= ad57bc1cca9482f78c6fb3271f43488a40ccdb2d
AKO_SRC_DIR ?= $(BUILD_DIR)/mongodb-atlas-kubernetes-$(AKO_COMMIT)
AKO_RELEASE_VERSION ?= v2.13.2
AKO_LEGACY_CRDS_DIR ?= $(AKO_SRC_DIR)/releases/$(AKO_RELEASE_VERSION)/deploy/crds
ATLAS_DOMAIN ?= https://cloud.mongodb.com/
ATLAS_OPERATOR_NAMESPACE ?= mongodb-atlas-system
ATLAS_OPERATOR_SECRET_NAME ?= mongodb-atlas-operator-api-key

KCP_KUBECONFIG ?= $(BUILD_DIR)/kcp-admin.kubeconfig
KCP_FRONT_PROXY_EXTRA_SANS ?=

KO_DOCKER_REPO ?= kind.local
KIND_CLUSTER_NAME ?= dbaas
KO_PLATFORMS ?= linux/$(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

KUBECTL ?= kubectl
HELM ?= helm
KO ?= ko
KIND ?= kind
CLUSTERCTL ?= clusterctl
CURL ?= curl

$(BUILD_DIR):
	@mkdir -p $@

.PHONY: clean
clean: ## Remove all transient build artifacts
	rm -rf $(BUILD_DIR)

.PHONY: require-%
require-%:
	@command -v $* >/dev/null 2>&1 || { echo "$* is required but not installed."; exit 1; }

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_.-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
