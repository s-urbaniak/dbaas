# DBaaS - Database as a Service PoC

A proof-of-concept multi-tenant Database-as-a-Service built on
[KCP](https://kcp.io), [kro](https://kro.run), and MongoDB's Kubernetes
operators.

Consumers interact with a single `MongoDBDatabase` API. The platform routes
each request to either an on-prem MongoDB cluster via
[MCK](https://github.com/mongodb/mongodb-kubernetes) or an Atlas
`FlexCluster` via the [Atlas Kubernetes
Operator](https://github.com/mongodb/mongodb-atlas-kubernetes),
depending on `spec.provider`.

For the full architecture, authentication model, deploy internals, and
resource lifecycle, see [dbaas.md](dbaas.md).

## What This Demo Does

- provisions tenant workspaces under `root:consumers`
- exposes one tenant-facing API: `kro.run/v1alpha1 MongoDBDatabase`
- syncs tenant objects into the physical cluster through the API Sync Agent
- lets kro create backend child resources based on `spec.provider`
- exposes tenant workspaces in a small provisioner UI and in Headlamp

High-level flow:

1. A tenant creates a workspace in the provisioner UI.
2. The tenant downloads a kubeconfig or opens the workspace in Headlamp.
3. The tenant creates a `MongoDBDatabase`.
4. The API Sync Agent mirrors it into the physical cluster.
5. kro creates either a `MongoDB` or `FlexCluster` child resource.
6. Mock controllers write backend status and the result syncs back to KCP.

## Quick Start

### Prerequisites

- [kind](https://kind.sigs.k8s.io/) or minikube for the local Kubernetes
  cluster
- [helm](https://helm.sh/) >= 3.14 to install cert-manager, KCP, kro, and the
  API Sync Agent
- [ko](https://ko.build/) to build and load Go container images
- [kubectl](https://kubernetes.io/docs/tasks/tools/) to interact with the
  cluster and KCP
- [clusterctl](https://cluster-api.sigs.k8s.io/clusterctl/overview) v1.12.x
  to bootstrap Cluster API and CAPD into the local kind cluster
- Python >= 3.10 for the deploy UI and Headlamp kubeconfig bootstrap scripts
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

This runs the full pipeline:

```text
kind -> helm-repos -> cert-manager -> capi -> kcp -> crds -> kro -> kubeconfig
-> bootstrap -> sync-agent -> provisioner -> controllers -> headlamp
```

No port-forwarding is needed. The kind cluster exposes:

| Service | Host URL |
|---|---|
| KCP front-proxy | `https://localhost:6443` |
| Provisioner UI | `http://localhost:8090` |
| Headlamp GUI | `http://localhost:4466` |

Open `http://localhost:8090` once the pipeline completes.

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

For newly provisioned workspaces, the provisioner creates a workspace-local
service account, token secret, and `ClusterRoleBinding`, and builds the
downloaded kubeconfig and Headlamp context from that token.

### Create a database

```bash
export KUBECONFIG=/path/to/tenant-a.kubeconfig

kubectl apply -f - <<'EOF'
apiVersion: kro.run/v1alpha1
kind: MongoDBDatabase
metadata:
  name: my-onprem-db
  namespace: default
spec:
  provider: ON-PREMISE
  region: DC_FRANKFURT
  version: "7.0"
  members: 3
  storage: 10Gi
EOF
```

Atlas example:

```bash
kubectl apply -f - <<'EOF'
apiVersion: kro.run/v1alpha1
kind: MongoDBDatabase
metadata:
  name: my-atlas-db
  namespace: default
spec:
  provider: AWS
  region: US_EAST_1
  members: 3
EOF
```

### Inspect status

```bash
kubectl get mongodbdatabases
kubectl get mongodatabase my-onprem-db -o jsonpath='{.status}'
```

For physical-cluster objects:

```bash
unset KUBECONFIG
kubectl get mongodb,flexclusters -A
```

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
go run ./cmd/mock-flexcluster/
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

```text
├── cmd/
│   ├── mock-mongodb/
│   ├── mock-flexcluster/
│   └── provisioner/
├── internal/
│   ├── controller/
│   └── provisioner/
├── mk/
├── config/
│   ├── kro/
│   ├── mck-crds/
│   ├── atlas-crds/
│   └── sync-agent/
├── deploy/
│   ├── capi/
│   ├── kind/
│   ├── kcp/
│   ├── kro/
│   ├── headlamp/
│   ├── mock-mongodb/
│   ├── mock-flexcluster/
│   └── provisioner/
├── scripts/
├── headlamp-plugin/kcp/
├── dbaas.md
├── Makefile
└── go.mod
```

## Further Reading

- [dbaas.md](dbaas.md) for the full architecture and operations guide
- [Makefile](Makefile) for the deploy pipeline
- `config/kro/mongodatabase-rgd.yaml` for the generated `MongoDBDatabase` API

Current known limitations:

- kro generates the tenant CRD in the `kro.run` API group, not
  `dbaas.mongodb.com`
- the current graph status mainly reflects the on-prem branch
- Headlamp still uses one shared deployment with provisioner-managed contexts,
  not per-user login
