CAPI_VERSION ?= v1.12.4
CAPI_QUICKSTART_CLUSTER_NAME ?= capd-quickstart
CAPI_QUICKSTART_KUBERNETES_VERSION ?= v1.32.1
CAPI_ADDON_NAMESPACE ?= default
CAPI_CNI_LABEL_KEY ?= addons.dbaas.dev/cni
CAPI_CNI_LABEL_VALUE ?= calico

.PHONY: check-capd-host
check-capd-host: ## Verify the host sysctl limits required by CAPD
	@if [ "$$(uname -s)" != "Linux" ]; then exit 0; fi; \
	 WATCHES="$$(sysctl -n fs.inotify.max_user_watches 2>/dev/null || echo 0)"; \
	 INSTANCES="$$(sysctl -n fs.inotify.max_user_instances 2>/dev/null || echo 0)"; \
	 if [ "$$WATCHES" -lt 1048576 ] || [ "$$INSTANCES" -lt 8192 ]; then \
	   echo "CAPD requires higher host inotify limits."; \
	   echo "Current values: fs.inotify.max_user_watches=$$WATCHES fs.inotify.max_user_instances=$$INSTANCES"; \
	   echo "Persist them across reboots with:"; \
	   echo "  sudo tee /etc/sysctl.d/99-dbaas-capd.conf >/dev/null <<'EOF'"; \
	   echo "  fs.inotify.max_user_watches = 1048576"; \
	   echo "  fs.inotify.max_user_instances = 8192"; \
	   echo "  EOF"; \
	   echo "  sudo sysctl --system"; \
	   exit 1; \
	 fi

.PHONY: check-clusterctl
check-clusterctl: ## Verify clusterctl is installed at the expected version
	@command -v $(CLUSTERCTL) >/dev/null 2>&1 || { \
	  echo "clusterctl is required but not installed."; \
	  echo "Install Cluster API clusterctl $(CAPI_VERSION) and retry."; \
	  exit 1; \
	}
	@VERSION="$$($(CLUSTERCTL) version | tr '\n' ' ')"; \
	 echo "Using $$VERSION"; \
	 echo "$$VERSION" | grep -Eq 'v1\.12\.' || { \
	   echo "clusterctl v1.12.x is required."; \
	   exit 1; \
	 }

.PHONY: deploy-capi undeploy-capi
deploy-capi: check-clusterctl check-capd-host ## Install Cluster API and the Docker provider
	CLUSTER_TOPOLOGY=true $(CLUSTERCTL) init \
	  --core cluster-api:$(CAPI_VERSION) \
	  --bootstrap kubeadm:$(CAPI_VERSION) \
	  --control-plane kubeadm:$(CAPI_VERSION) \
	  --infrastructure docker:$(CAPI_VERSION)
	$(KUBECTL) -n capi-system rollout status deployment/capi-controller-manager --timeout=300s
	$(KUBECTL) -n capi-kubeadm-bootstrap-system rollout status deployment/capi-kubeadm-bootstrap-controller-manager --timeout=300s
	$(KUBECTL) -n capi-kubeadm-control-plane-system rollout status deployment/capi-kubeadm-control-plane-controller-manager --timeout=300s
	$(KUBECTL) -n capd-system rollout status deployment/capd-controller-manager --timeout=300s
	bash $(SCRIPTS_DIR)/upsert_configmap.sh \
	  "$(KUBECTL)" \
	  "$(CAPI_ADDON_NAMESPACE)" \
	  dbaas-calico \
	  calico.yaml \
	  deploy/capi/calico.yaml
	$(KUBECTL) apply -f deploy/capi/clusterresourceset.yaml
	@echo "✓ Cluster API + CAPD ready"

undeploy-capi: check-clusterctl ## Remove Cluster API, CAPD, and Calico addon bootstrap
	$(KUBECTL) -n $(CAPI_ADDON_NAMESPACE) delete clusterresourceset dbaas-calico --ignore-not-found
	$(KUBECTL) -n $(CAPI_ADDON_NAMESPACE) delete configmap dbaas-calico --ignore-not-found
	$(CLUSTERCTL) delete --all --include-crd || true

.PHONY: capd-quickstart-up capd-quickstart-down
capd-quickstart-up: check-clusterctl check-capd-host ## Create a demo CAPD workload cluster
	$(CLUSTERCTL) generate cluster $(CAPI_QUICKSTART_CLUSTER_NAME) \
	  --infrastructure docker \
	  --flavor development \
	  --kubernetes-version $(CAPI_QUICKSTART_KUBERNETES_VERSION) \
	  --control-plane-machine-count 1 \
	  --worker-machine-count 1 | $(KUBECTL) apply -f -
	$(KUBECTL) label cluster $(CAPI_QUICKSTART_CLUSTER_NAME) \
	  $(CAPI_CNI_LABEL_KEY)=$(CAPI_CNI_LABEL_VALUE) --overwrite
	@echo "✓ CAPD quickstart cluster applied"
	@echo "  Inspect with: $(CLUSTERCTL) describe cluster $(CAPI_QUICKSTART_CLUSTER_NAME)"

capd-quickstart-down: ## Delete the demo CAPD workload cluster
	$(KUBECTL) delete cluster $(CAPI_QUICKSTART_CLUSTER_NAME) --ignore-not-found
	@while $(KUBECTL) get cluster $(CAPI_QUICKSTART_CLUSTER_NAME) >/dev/null 2>&1; do sleep 2; done
	@echo "✓ CAPD quickstart cluster removed"
