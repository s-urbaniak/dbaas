# DBaaS - Database as a Service PoC

A proof-of-concept multi-tenant DBaaS and Kubernetes playground built on
[kcp](https://kcp.io), [kro](https://kro.run), Cluster API, and MongoDB's
Kubernetes operators.

Consumers currently interact with two tenant-facing APIs:

- `kro.run/v1alpha1 Database`
- `kro.run/v1alpha1 Kubernetes`

`Database` routes each request to either an on-prem MongoDB cluster via
[MCK](https://github.com/mongodb/mongodb-kubernetes) or an Atlas generated
resource via the [Atlas Kubernetes
Operator](https://github.com/mongodb/mongodb-atlas-kubernetes), depending on
`spec.provider`. Cloud-backed databases can render either a generated
`FlexCluster` or a generated `Cluster` configured as an M0 shared cluster.

`Kubernetes` provisions a CAPD-backed workload cluster and mounts it back into
the tenant workspace as a child workspace so the tenant can `kubectl` into it
through kcp.

For the full architecture, authentication model, deploy internals, and
resource lifecycle, see [dbaas.md](dbaas.md).

## What This Demo Does

- provisions tenant workspaces under `root:consumers`
- exposes two tenant-facing APIs: `kro.run/v1alpha1 Database` and
  `kro.run/v1alpha1 Kubernetes`
- syncs tenant objects into the physical cluster through the API Sync Agent
- lets kro create backend child resources based on the tenant spec
- exposes tenant workspaces in a small provisioner UI and in Headlamp

High-level flow:

1. A tenant creates a workspace in the provisioner UI.
2. The tenant downloads a kubeconfig or opens the workspace in Headlamp.
3. The tenant creates a `Database` or `Kubernetes` resource.
4. The API Sync Agent mirrors it into the physical cluster.
5. kro creates the backing resources.
6. The mock MongoDB controller, the real Atlas operator, or the
   `kubernetes-controller` write backend status and the result syncs back to
   kcp.

## Quick Start

### Prerequisites

- [kind](https://kind.sigs.k8s.io/) or minikube for the local Kubernetes
  cluster
- [helm](https://helm.sh/) >= 3.14 to install cert-manager, kcp, kro, and the
  API Sync Agent
- [ko](https://ko.build/) to build and load Go container images
- [kubectl](https://kubernetes.io/docs/tasks/tools/) to interact with the
  cluster and kcp
- [clusterctl](https://cluster-api.sigs.k8s.io/clusterctl/overview) v1.12.x
  to bootstrap Cluster API and CAPD into the local kind cluster
- Python >= 3.10 for the staged `make deploy` helper script
- Go >= 1.22 for local builds

Linux hosts using CAPD also need higher inotify limits persisted across
reboots:

```bash
sudo tee /etc/sysctl.d/99-dbaas-capd.conf >/dev/null <<'EOF'
fs.inotify.max_user_watches = 1048576
fs.inotify.max_user_instances = 8192
EOF

sudo sysctl --system
```

### Deploy everything

```bash
make deploy
```

For Atlas-backed databases, export credentials before deploy:

```bash
export ATLAS_ORG_ID=...
export ATLAS_PUBLIC_KEY=...
export ATLAS_PRIVATE_KEY=...
```

This runs the full pipeline:

```text
kind -> helm-repos -> cert-manager -> capi -> kcp -> crds -> kro -> kubeconfig
-> bootstrap -> sync-agent -> provisioner -> controllers -> atlas -> headlamp
```

No port-forwarding is needed. The kind cluster exposes:

| Service | Host URL |
|---|---|
| kcp front-proxy | `https://localhost:6443` |
| Provisioner UI | `http://localhost:8090` |
| Headlamp GUI | `http://localhost:4466` |

Open `http://localhost:8090` once the pipeline completes.

If you open the provisioner through `localhost`, `127.0.0.1`, or another host
IP such as `192.168.178.201`, the downloaded kubeconfigs and Headlamp links use
that same host with ports `6443` and `4466`.

To make the kcp front-proxy certificate valid for additional hostnames or IPs,
set `KCP_FRONT_PROXY_EXTRA_SANS` when deploying kcp:

```bash
make deploy-kcp \
  KCP_FRONT_PROXY_EXTRA_SANS=192.168.178.201,127.0.0.1,localhost
```

The same kind cluster also acts as a Cluster API management cluster. `make
deploy` installs Cluster API core providers, the Docker infrastructure
provider (CAPD), and a `ClusterResourceSet` that applies Calico to
repo-created workload clusters labeled `addons.dbaas.dev/cni=calico`.

To tear everything down:

```bash
make undeploy
make kind-delete
```

## Usage

### Provision a consumer workspace

1. Open `http://localhost:8090`.
2. Enter a workspace name such as `tenant-a`.
3. Wait for the workspace to become `Ready`.
4. Click `kubeconfig` to download credentials or `Headlamp` to open the
   workspace in the GUI.

If you opened the UI through a different hostname or IP, those links are
rendered with that same host automatically.

For newly provisioned workspaces, the provisioner creates a workspace-local
service account, token secret, and `ClusterRoleBinding`, and builds the
downloaded kubeconfig and Headlamp context from that token. It also reconciles
both the `dbaas` and `kubernetes` APIBindings in consumer workspaces.

### Create a database

```bash
export KUBECONFIG=/path/to/tenant-a.kubeconfig

kubectl apply -f - <<'EOF'
apiVersion: kro.run/v1alpha1
kind: Database
metadata:
  name: my-onprem-db
  namespace: default
spec:
  type: mongodb
  class: on-premise
  version: "7.0"
  members: 3
  storage: 10Gi
EOF
```

Atlas example:

```bash
kubectl apply -f - <<'EOF'
apiVersion: kro.run/v1alpha1
kind: Database
metadata:
  name: my-atlas-db
  namespace: default
spec:
  type: mongodb
  class: cloud
  provider: AWS
  tier: FLEX
  region: US_EAST_1
EOF
```

Atlas M0 example:

```bash
kubectl apply -f - <<'EOF'
apiVersion: kro.run/v1alpha1
kind: Database
metadata:
  name: my-atlas-m0-db
  namespace: default
spec:
  type: mongodb
  class: cloud
  provider: AWS
  tier: M0
  region: US_EAST_1
EOF
```

### Inspect status

```bash
kubectl get databases
kubectl get database my-onprem-db -o jsonpath='{.status}'
```

For physical-cluster objects:

```bash
unset KUBECONFIG
kubectl get mongodb,groups,clusters,flexclusters -A
```

### Create a tenant Kubernetes cluster

```bash
export KUBECONFIG=/path/to/tenant-a.kubeconfig

kubectl apply -f - <<'EOF'
apiVersion: kro.run/v1alpha1
kind: Kubernetes
metadata:
  name: demo-cluster
  namespace: default
spec:
  machineCount:
    controlPlane: 1
    worker: 1
EOF
```

Optional field:

- `spec.allowSchedulingOnControlPlanes` defaults to `true`

Inspect it:

```bash
kubectl get kubernetes
kubectl get kubernetes demo-cluster -o yaml
```

Once the resource reaches `status.phase=Ready`, the provisioned cluster is
mounted back into the tenant workspace as a child workspace with the same name.
From the tenant workspace, switch to that child workspace and use it like a
normal Kubernetes cluster.

## Development

List the available top-level make targets:

```bash
make help
```

### Refresh upstream CRDs

```bash
make refresh-crds
```

### Run controllers locally

```bash
go run ./cmd/mock-mongodb/
```

### Run the provisioner locally

```bash
go run ./cmd/provisioner/ --kubeconfig=/tmp/kcp-admin.kubeconfig
```

The UI is then available at `http://localhost:8090`.

### Rebuild and reload images into kind

```bash
KO_DOCKER_REPO=kind.local make ko-apply
```

### Smoke-test CAPD

Create a demo workload cluster managed by CAPD:

```bash
make capd-quickstart-up
clusterctl describe cluster capd-quickstart
```

`make capd-quickstart-up` labels the workload cluster so the management
cluster installs Calico automatically. The target also fails fast if the
host inotify limits are below the required CAPD values.

Delete it again:

```bash
make capd-quickstart-down
```

## Repository Layout

- `cmd/`: service entrypoints for the mock controller, Kubernetes controller,
  and provisioner.
- `config/`: checked-in CRDs, kro graphs, and sync-agent resource manifests.
- `deploy/`: deploy-time manifests and overlays for the local platform
  components.
- `headlamp-plugin/`: custom Headlamp plugin code for the kcp workspace UI.
- `internal/`: controller and provisioner implementation packages.
- `mk/`: makefile fragments for build, deploy, and teardown workflows.
- `scripts/`: small helper scripts used by the repo.
- `dbaas.md`: architecture and operations notes.
- `Makefile`: top-level entrypoint for the local deployment workflow.
- `go.mod`: Go module definition and dependency roots.

## Further Reading

- [dbaas.md](dbaas.md) for the full architecture and operations guide
- [Makefile](Makefile) for the deploy pipeline
- `config/kro/database-rgd.yaml` for the `Database` API

Current known limitations:

- kro generates the tenant CRD in the `kro.run` API group, not
  `dbaas.mongodb.com`
- KRO 0.9.1 currently drops RGD field descriptions from generated CRDs, so
  `kubectl explain` does not yet show the doc strings declared in the RGDs
- the current graph status mainly reflects the on-prem branch
- Headlamp still uses one shared deployment with provisioner-managed contexts,
  not per-user login
