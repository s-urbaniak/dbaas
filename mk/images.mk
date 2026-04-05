.PHONY: ko-build ko-apply
ko-build: ## Build the local controller and provisioner images with ko
	KO_DOCKER_REPO=$(KO_DOCKER_REPO) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	  $(KO) build --platform=$(KO_PLATFORMS) \
	  ./cmd/mock-mongodb/ \
	  ./cmd/kubernetes-controller/ \
	  ./cmd/provisioner/

ko-apply: ## Build and apply the local controller and provisioner manifests with ko
	KO_DOCKER_REPO=$(KO_DOCKER_REPO) KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	  $(KO) apply --platform=$(KO_PLATFORMS) \
	  -f deploy/mock-mongodb/ \
	  -f deploy/kubernetes-controller/ \
	  -f deploy/provisioner/

.PHONY: build vet
build: ## Build all Go packages in the repository
	go build ./...

vet: ## Run go vet on all Go packages
	go vet ./...
