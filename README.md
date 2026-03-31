# DBaaS — Database as a Service PoC

A proof-of-concept multi-tenant Database-as-a-Service built on [KCP](https://kcp.io), [kro](https://kro.run), and MongoDB's Kubernetes operators.

Consumers interact with a single, simplified `MongoDBDatabase` API. Behind the scenes the platform routes their request to either an on-premise MongoDB cluster (via [MCK](https://github.com/mongodb/mongodb-kubernetes)) or a cloud Atlas cluster (via [Atlas Kubernetes Operator](https://github.com/mongodb/mongodb-atlas-kubernetes)), depending on the `provider` field.

---

## Architecture

```
Physical Kubernetes Cluster (kind / minikube)
│
│  ┌─── KCP (Helm, StatefulSet + built-in etcd) ──────────────────┐
│  │                                                               │
│  │  root:dbaas-provider  (service-provider workspace)           │
│  │    └── APIExport "mongodatabases.dbaas.mongodb.com"          │
│  │         (published by the API Sync Agent)                    │
│  │                                                               │
│  │  root:consumers  (parent org workspace)                      │
│  │    └── root:consumers:<tenant>                               │
│  │          └── APIBinding → dbaas-provider                     │
│  │          └── MongoDBDatabase  ← only visible resource        │
│  └───────────────────────────────────────────────────────────── ┘
│
│  ┌─── API Sync Agent ─────────────────────────────────────────── ┐
│  │  Syncs MongoDBDatabase instances between KCP workspaces       │
│  │  and the physical cluster (bidirectional, including status)   │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── kro ────────────────────────────────────────────────────── ┐
│  │  ResourceGraphDefinition → auto-generates MongoDBDatabase CRD │
│  │  provider=ON-PREMISE  → creates mongodb.com/v1 MongoDB        │
│  │  provider=AWS|AZURE   → creates atlas FlexCluster             │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── mock-mongodb-controller ────────────────────────────────── ┐
│  │  Reconciles mongodb.com/v1 MongoDB                            │
│  │  Sets status.phase=Running, Ready condition                   │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── mock-flexcluster-controller ────────────────────────────── ┐
│  │  Reconciles atlas.generated.mongodb.com/v1 FlexCluster        │
│  │  Sets status.v20250312.stateName=IDLE, Ready condition        │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── provisioner web server ─────────────────────────────────── ┐
│  │  POST /api/workspaces              → provision consumer WS    │
│  │  GET  /api/workspaces/{n}/kubeconfig → download kubeconfig    │
│  │  GET  /                            → HTML management UI       │
│  └────────────────────────────────────────────────────────────── ┘
```

### End-to-end flow

1. Admin opens the provisioner UI, creates a workspace for a tenant.
2. Tenant downloads their kubeconfig and applies a `MongoDBDatabase` manifest.
3. The API Sync Agent syncs the resource to the physical cluster.
4. kro's microcontroller creates the appropriate backend resource (`MongoDB` or `FlexCluster`).
5. The mock controller reconciles the backend and writes status.
6. kro propagates status → `MongoDBDatabase.status`.
7. The sync agent pushes status back up to the tenant's KCP workspace.
8. Tenant runs `kubectl get mongodatabase my-db` and sees `state` + `connectionString`.

---

## Repository layout

```
├── cmd/
│   ├── mock-mongodb/       # mock MCK MongoDB controller (ko image)
│   ├── mock-flexcluster/   # mock Atlas FlexCluster controller (ko image)
│   └── provisioner/        # workspace provisioner HTTP server (ko image)
│       └── static/         # embedded HTML UI template
├── internal/
│   ├── controller/         # shared reconciler implementations (unstructured)
│   └── provisioner/        # KCP workspace + APIBinding + kubeconfig logic
├── config/
│   ├── kro/                # kro ResourceGraphDefinition for MongoDBDatabase
│   ├── mck-crds/           # MCK CRDs (refreshed from source repo)
│   ├── atlas-crds/         # Atlas FlexCluster CRD (refreshed from source repo)
│   └── sync-agent/         # API Sync Agent PublishedResource manifest
├── deploy/
│   ├── kcp/                # KCP Helm values + workspace bootstrap manifest
│   ├── kro/                # kro Helm values
│   ├── mock-mongodb/       # RBAC + Deployment
│   ├── mock-flexcluster/   # RBAC + Deployment
│   └── provisioner/        # Deployment + Service
├── hack/
│   └── boilerplate.go.txt
├── dbaas.md                # detailed design doc with rationale
├── Makefile
└── go.mod
```

---

## Prerequisites

| Tool | Purpose |
|---|---|
| [kind](https://kind.sigs.k8s.io/) or minikube | local Kubernetes cluster |
| [helm](https://helm.sh/) ≥ 3.14 | install KCP, kro, API Sync Agent |
| [ko](https://ko.build/) | build + load Go container images |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | interact with cluster and KCP |
| Go ≥ 1.22 | build locally |

---

## Deploy

### 1 — Start a local cluster

```bash
kind create cluster --name dbaas
```

### 2 — Deploy the full stack

```bash
# installs KCP, kro, API Sync Agent, mock controllers, provisioner
make deploy
```

This runs the following steps in order (order matters — see `dbaas.md`):

```
deploy-kcp → apply-crds → deploy-kro → deploy-sync-agent → ko-apply
```

> **Note:** `deploy-kro` must finish before `deploy-sync-agent`. kro dynamically
> creates the `MongoDBDatabase` CRD; the sync agent needs it to exist.

### 3 — Bootstrap KCP workspaces

```bash
# extract the KCP admin kubeconfig
make get-kcp-kubeconfig      # writes to /tmp/kcp-admin.kubeconfig

# create root:dbaas-provider and root:consumers in KCP
make bootstrap-kcp-workspaces
```

### 4 — Expose the provisioner

```bash
# make the KCP admin kubeconfig available to the provisioner pod
kubectl create secret generic kcp-admin-kubeconfig \
  --from-file=kubeconfig=/tmp/kcp-admin.kubeconfig

kubectl rollout restart deployment/dbaas-provisioner
kubectl port-forward svc/dbaas-provisioner 8090
```

Open **http://localhost:8090** in your browser.

---

## Usage

### Provision a consumer workspace

1. Open http://localhost:8090.
2. Enter a workspace name (e.g. `tenant-a`) and click **Provision**.
3. Once the workspace is `Ready`, click **↓ kubeconfig** to download.

### Create a database

```bash
export KUBECONFIG=/path/to/tenant-a.kubeconfig

# on-premise MongoDB (routes to MCK)
kubectl apply -f - <<EOF
apiVersion: dbaas.mongodb.com/v1alpha1
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

# cloud Atlas FlexCluster (routes to Atlas operator)
kubectl apply -f - <<EOF
apiVersion: dbaas.mongodb.com/v1alpha1
kind: MongoDBDatabase
metadata:
  name: my-atlas-db
  namespace: default
spec:
  provider: AWS
  region: US_EAST_1
EOF
```

### Inspect status

```bash
kubectl get mongodatabase
# NAME           PROVIDER     REGION       STATE     READY
# my-onprem-db   ON-PREMISE   DC_FRANKFURT Running   True
# my-atlas-db    AWS          US_EAST_1    IDLE      True

kubectl get mongodatabase my-onprem-db -o jsonpath='{.status}'
```

### Inspect backend resources (physical cluster)

```bash
# switch back to the cluster kubeconfig
unset KUBECONFIG

kubectl get mongodb,flexclusters -A
```

---

## Development

### Refresh upstream CRDs

```bash
# re-copy MCK and Atlas CRDs from their source repos
make refresh-crds
```

### Run controllers locally

```bash
# against current KUBECONFIG cluster
go run ./cmd/mock-mongodb/
go run ./cmd/mock-flexcluster/
```

### Run the provisioner locally

```bash
go run ./cmd/provisioner/ --kubeconfig=/tmp/kcp-admin.kubeconfig
# → http://localhost:8090
```

### Rebuild and reload images into kind

```bash
KO_DOCKER_REPO=kind.local make ko-apply
```

---

## Design notes

See [`dbaas.md`](dbaas.md) for the full design document, including:

- Why kro over Crossplane
- KCP workspace hierarchy rationale (Universal vs custom WorkspaceType)
- Provisioner auth — dev token vs production OIDC
- Sync agent startup ordering constraint
