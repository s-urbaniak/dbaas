# DBaaS Sample Provider Plan (v2 — revised topology)

## Context

Build a sample Database-as-a-Service provider using KCP as the multi-tenant control plane.

**Revised design goals (from the original):**
- **kro** (Kube Resource Orchestrator) synthesizes `MongoDBDatabase` from backend CRDs via a declarative `ResourceGraphDefinition` — no hand-rolled Go routing controller needed
- Two **focused mock controllers**: one for `mongodb.com/v1 MongoDB`, one for `atlas.generated.mongodb.com/v1 FlexCluster`
- A proper KCP **workspace hierarchy** with a service-provider workspace and a consumer org workspace
- A **provisioner web server** (Go + HTML) that creates consumer workspaces, binds the DBaaS APIExport, and returns a kubeconfig

**Target directory:** `/Users/s.urbaniak/src/dbaas`
**Target cluster:** Local (kind/minikube)

---

## Architecture Overview

```
Physical Kubernetes Cluster (kind/minikube)
│
│  ┌─── KCP (Helm, StatefulSet + built-in etcd) ──────────────────┐
│  │                                                               │
│  │  root:dbaas-provider  (service-provider workspace)           │
│  │    └── APIExport "mongodatabases.dbaas.mongodb.com"          │
│  │         (created by API Sync Agent via PublishedResource)    │
│  │                                                               │
│  │  root:consumers  (org workspace — consumer parent)           │
│  │    ├── root:consumers:tenant-a                               │
│  │    │     └── APIBinding → dbaas-provider/mongodatabases      │
│  │    │     └── MongoDBDatabase (only visible resource)         │
│  │    └── root:consumers:tenant-b  (etc.)                       │
│  └───────────────────────────────────────────────────────────── ┘
│
│  ┌─── API Sync Agent ─────────────────────────────────────────── ┐
│  │  PublishedResource: mongodatabases.dbaas.mongodb.com          │
│  │  ↕ syncs MongoDBDatabase instances between KCP & cluster      │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── kro ────────────────────────────────────────────────────── ┐
│  │  ResourceGraphDefinition → generates MongoDBDatabase CRD      │
│  │  Microcontroller: on create/update MongoDBDatabase:           │
│  │    includeWhen provider=ON-PREMISE  → mongodb.com/v1 MongoDB  │
│  │    includeWhen provider=AWS|AZURE   → FlexCluster             │
│  │  Aggregates child status → MongoDBDatabase.status             │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── mock-mongodb-controller (ko) ──────────────────────────── ┐
│  │  Watches: mongodb.com/v1 MongoDB                              │
│  │  Sets: status.phase=Running, Ready condition                  │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── mock-flexcluster-controller (ko) ──────────────────────── ┐
│  │  Watches: atlas.generated.mongodb.com/v1 FlexCluster          │
│  │  Sets: status.v20250312.stateName=IDLE, Ready condition       │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── provisioner web server (ko) ───────────────────────────── ┐
│  │  POST /api/workspaces  → creates KCP workspace + APIBinding   │
│  │  GET  /api/workspaces/{name}/kubeconfig  → kubeconfig YAML    │
│  │  GET  /  → HTML UI: provision workspace + download kubeconfig │
│  └────────────────────────────────────────────────────────────── ┘
```

**End-to-end flow:**
1. Admin provisions consumer workspace via web UI (creates `root:consumers:tenant-x` + `APIBinding`)
2. Consumer downloads kubeconfig, applies `MongoDBDatabase` YAML
3. API Sync Agent syncs it to the physical cluster
4. kro's microcontroller creates the appropriate backend resource (MCK or FlexCluster)
5. Mock controller reconciles the backend, writes status
6. kro aggregates status → `MongoDBDatabase.status`
7. Sync agent propagates status back up to the KCP workspace
8. Consumer does `kubectl get mongodatabase my-db` and sees state + connectionString

---

## Revised directory structure

Files to **remove** from the current scaffold:
- `api/v1alpha1/` — kro generates the `MongoDBDatabase` CRD; no hand-coded Go types needed
- `internal/controller/mongodatabase_controller.go` — kro replaces the routing logic
- `cmd/controller/` — replaced by focused `cmd/mock-mongodb/` and `cmd/mock-flexcluster/`

Files to **add**:

```
dbaas/
├── Makefile  (updated)
├── go.mod    (updated — drops controller-gen dep, adds net/http for provisioner)
│
├── cmd/
│   ├── mock-mongodb/
│   │   └── main.go          # controller-runtime manager for MCK MongoDB
│   ├── mock-flexcluster/
│   │   └── main.go          # controller-runtime manager for Atlas FlexCluster
│   └── provisioner/
│       ├── main.go          # HTTP server (workspace CRUD + kubeconfig)
│       └── static/
│           └── index.html   # minimal HTML UI
│
├── internal/
│   ├── controller/
│   │   ├── mongodb_controller.go       # reconciles mongodb.com/v1 MongoDB
│   │   └── flexcluster_controller.go  # reconciles atlas FlexCluster
│   └── provisioner/
│       └── workspace.go    # KCP workspace + APIBinding creation logic
│
├── config/
│   ├── kro/
│   │   └── mongodatabase-rgd.yaml    # kro ResourceGraphDefinition
│   ├── mck-crds/                     # (already copied)
│   ├── atlas-crds/                   # (already copied)
│   └── sync-agent/
│       └── mongodatabase-published.yaml
│
└── deploy/
    ├── kcp/
    │   ├── namespace.yaml
    │   ├── kcp-values.yaml
    │   └── workspaces.yaml    # root:dbaas-provider + root:consumers bootstrapping
    ├── kro/
    │   └── kro-values.yaml    # kro Helm values
    ├── mock-mongodb/
    │   └── deployment.yaml
    ├── mock-flexcluster/
    │   └── deployment.yaml
    └── provisioner/
        └── deployment.yaml
```

---

## Component Details

### 1. kro — ResourceGraphDefinition (replaces Go CRD types + routing controller)

**Rationale for choosing kro over Crossplane:**
kro uses declarative CEL expressions (`includeWhen`, status aggregation) and requires zero Go code — the entire `MongoDBDatabase` CRD definition, conditional child-resource creation, and status roll-up live in one YAML file. Crossplane would require a `CompositeResourceDefinition`, a `Composition`, and at least one Go or Python composition function to express the `provider`-based branching logic. kro is also more architecturally aligned with KCP: both treat the Kubernetes API as the composition surface rather than adding a separate control plane layer on top.

The main risk with kro is that `includeWhen` and CEL status aggregation are still alpha-level features. The first thing to validate after install is that the conditional child creation and `?? / null`-coalescing in the status block work as expected for this use case. If kro proves problematic, the fallback is a small Go controller (like the original `mongodatabase_controller.go`) — but try kro first.

**Install:**
```bash
helm repo add kro https://kro.run/charts
helm upgrade --install kro kro/kro -n kro --create-namespace
```

**`config/kro/mongodatabase-rgd.yaml`:**
```yaml
apiVersion: kro.run/v1alpha1
kind: ResourceGraphDefinition
metadata:
  name: mongodatabases.dbaas.mongodb.com
spec:
  schema:
    apiVersion: dbaas.mongodb.com/v1alpha1
    kind: MongoDBDatabase
    spec:
      provider: { type: string, enum: [ON-PREMISE, AWS, AZURE] }
      region:   { type: string }
      version:  { type: string, default: "7.0" }
      members:  { type: integer, default: 3 }
      storage:  { type: string, default: "10Gi" }
    status:
      state:            { type: string }
      connectionString: { type: string }

  resources:
    # ON-PREMISE → MCK MongoDB
    - id: mckMongoDB
      includeWhen:
        - ${schema.spec.provider == "ON-PREMISE"}
      template:
        apiVersion: mongodb.com/v1
        kind: MongoDB
        metadata:
          name: ${schema.metadata.name}
        spec:
          type: ReplicaSet
          members: ${schema.spec.members}
          version: ${schema.spec.version}

    # AWS or AZURE → Atlas FlexCluster
    - id: atlasFlexCluster
      includeWhen:
        - ${schema.spec.provider == "AWS" || schema.spec.provider == "AZURE"}
      template:
        apiVersion: atlas.generated.mongodb.com/v1
        kind: FlexCluster
        metadata:
          name: ${schema.metadata.name}
        spec:
          v20250312:
            entry:
              name: ${schema.metadata.name}
              providerSettings:
                backingProviderName: ${schema.spec.provider}
                regionName: ${schema.spec.region}

  # Aggregate status from whichever child was created
  status:
    state: >
      ${mckMongoDB != null ? mckMongoDB.status.phase :
        atlasFlexCluster != null ? atlasFlexCluster.status.v20250312.stateName :
        "UNKNOWN"}
    connectionString: >
      ${mckMongoDB != null ?
        "mongodb://" + schema.metadata.name + "." + schema.metadata.namespace + ".svc:27017" :
        "mongodb+srv://" + schema.metadata.name + ".atlas.mongodb.com"}
```

kro auto-generates the `mongodatabases.dbaas.mongodb.com` CRD and runs a microcontroller for it — no Go types or `controller-gen` needed.

---

### 2. Mock MongoDB controller (`cmd/mock-mongodb/`)

Uses `controller-runtime` with an unstructured client. Watches `mongodb.com/v1 MongoDB`.

**Reconcile loop:**
1. Get `MongoDB` object (unstructured)
2. Set `status.phase = "Running"`
3. Set `status.version = spec.version`
4. Set condition `type: Ready, status: True`
5. Patch status

No typed Go structs needed — all via `unstructured.Unstructured` + `SetNestedField`.

---

### 3. Mock FlexCluster controller (`cmd/mock-flexcluster/`)

Mirrors the MongoDB mock. Watches `atlas.generated.mongodb.com/v1 FlexCluster`.

**Reconcile loop:**
1. Get `FlexCluster` (unstructured)
2. Set `status.v20250312.stateName = "IDLE"`
3. Set conditions `[{type: Ready, status: True}, {type: State, reason: IDLE}]`
4. Patch status

---

### 4. KCP workspace hierarchy

**Rationale — Universal workspaces over custom WorkspaceTypes:**
KCP supports custom `WorkspaceType` objects with initializers — controllers that run once when a workspace is created to bootstrap resources (e.g., auto-create the `APIBinding`, set up RBAC). That would be the production-grade approach: define a `consumer` WorkspaceType whose initializer automatically creates the `APIBinding` to `root:dbaas-provider`, so consumers never need to do it manually.

For this PoC we use plain Universal workspaces and let the provisioner web server create the `APIBinding` imperatively. This keeps the workspace topology simple and avoids writing a WorkspaceType initializer controller. If you later want self-service workspace creation (consumer creates their own workspace and the binding appears automatically), promote this to a custom `WorkspaceType`.

Three workspace levels (all post-KCP-install, created via kubectl against KCP):

| Workspace path | Type | Purpose |
|---|---|---|
| `root:dbaas-provider` | Universal | Holds APIExport (created by API Sync Agent) |
| `root:consumers` | Universal | Parent org for all tenant workspaces |
| `root:consumers:<name>` | Universal | Per-consumer, only sees MongoDBDatabase via APIBinding |

`deploy/kcp/workspaces.yaml` bootstraps `root:dbaas-provider` and `root:consumers`. Consumer workspaces are created dynamically by the provisioner.

**APIBinding spec in a consumer workspace:**
```yaml
apiVersion: apis.kcp.io/v1alpha1
kind: APIBinding
metadata:
  name: dbaas
spec:
  reference:
    export:
      name: mongodatabases.dbaas.mongodb.com
      path: root:dbaas-provider
```

---

### 5. API Sync Agent

**Startup ordering constraint:**
The sync agent's `PublishedResource` references `mongodatabases.dbaas.mongodb.com`. That CRD is created dynamically by kro when the `ResourceGraphDefinition` is applied — it does not exist before kro runs. If the sync agent starts before kro has created the CRD, it will fail to find the resource and the `PublishedResource` will sit in an error state.

The `make deploy` sequence enforces: `deploy-kro` (which applies the RGD and waits for kro to generate the CRD) before `deploy-sync-agent`. Do not reorder those steps. If you redeploy only the sync agent in isolation, verify `kubectl get crd mongodatabases.dbaas.mongodb.com` succeeds first.

Runs in the physical cluster, connected to `root:dbaas-provider` workspace.

**`config/sync-agent/mongodatabase-published.yaml`:**
```yaml
apiVersion: syncagent.kcp.io/v1alpha1
kind: PublishedResource
metadata:
  name: mongodatabases
  namespace: kcp
spec:
  resource:
    group: dbaas.mongodb.com
    resource: mongodatabases
    version: v1alpha1
  statusBackSync:
    enabled: true
```

Sync agent creates the `APIExport` in `root:dbaas-provider`; consumers bind to it.

---

### 6. Provisioner web server (`cmd/provisioner/`)

Go HTTP server (`net/http` + Go HTML templates). Mounts KCP admin kubeconfig from a Secret.

**Endpoints:**

| Method | Path | Action |
|---|---|---|
| `GET` | `/` | HTML page with workspace form + list |
| `POST` | `/api/workspaces` | Create workspace under `root:consumers`, create APIBinding |
| `GET` | `/api/workspaces/{name}/kubeconfig` | Return kubeconfig YAML for download |

**Workspace creation flow (`internal/provisioner/workspace.go`):**
1. Create `tenancy.kcp.io/v1beta1 Workspace` in `root:consumers`
2. Poll `status.phase == "Ready"` and extract `status.url`
3. Switch client context to the new workspace
4. Create `apis.kcp.io/v1alpha1 APIBinding` → `root:dbaas-provider/mongodatabases`
5. Return workspace name + URL

**Kubeconfig generation:**
- Server address = `workspace.status.url`
- CA cert = same as KCP admin kubeconfig's CA
- Auth = short-lived bearer token (for dev: reuse admin token)
- Emit standard kubeconfig YAML via `k8s.io/client-go/tools/clientcmd`

**Auth note — dev vs production:**
For this PoC the provisioner reuses the KCP admin token in every generated kubeconfig. This means every consumer has full admin access to their workspace (and technically to KCP root if the token is not scoped). It is acceptable for a local demo but must not leave the dev environment.

The production upgrade path: create a per-workspace Kubernetes `ServiceAccount` in the consumer workspace, generate a bound `TokenRequest` for it, and embed that token in the kubeconfig instead. Alternatively, front KCP with an OIDC provider and issue short-lived JWT tokens scoped to the specific workspace path. Neither is implemented here.

**UI:** Single HTML page. Form for workspace name → submit → show download link. No JS framework needed; plain HTML with one `<form>` and HTMX (optional) or a simple redirect.

---

## KCP setup and deploy sequence

```bash
# 1. Install KCP
make deploy-kcp

# 2. Bootstrap provider + consumer org workspaces in KCP
make bootstrap-kcp-workspaces

# 3. Install MCK + Atlas CRDs in physical cluster
make apply-crds

# 4. Install kro + apply ResourceGraphDefinition
make deploy-kro

# 5. Install API Sync Agent (connects to root:dbaas-provider)
make deploy-sync-agent

# 6. Deploy mock controllers + provisioner
make ko-apply
```

---

## Makefile targets

```makefile
MCK_SRC   ?= /Users/s.urbaniak/src/mongodb-kubernetes/config/crd/bases
ATLAS_SRC ?= /Users/s.urbaniak/src/mongodb-atlas-kubernetes/config/generated/crd/bases
KO_DOCKER_REPO ?= kind.local

refresh-mck-crds:   # cp from MCK_SRC
refresh-atlas-crds: # cp from ATLAS_SRC
refresh-crds: refresh-mck-crds refresh-atlas-crds

apply-mck-crds apply-atlas-crds apply-crds

deploy-kcp:            # helm kcp/kcp
bootstrap-kcp-workspaces: # kubectl apply -f deploy/kcp/workspaces.yaml (against KCP)
undeploy-kcp

deploy-kro:            # helm kro/kro + kubectl apply -f config/kro/
undeploy-kro

deploy-sync-agent:     # helm kcp/api-syncagent + kubectl apply -f config/sync-agent/
undeploy-sync-agent

ko-apply:              # KO_DOCKER_REPO=kind.local ko apply -f deploy/{mock-mongodb,mock-flexcluster,provisioner}/
ko-build

deploy: deploy-kcp apply-crds deploy-kro deploy-sync-agent ko-apply
undeploy
```

---

## Critical files

| File | What changes |
|---|---|
| `config/kro/mongodatabase-rgd.yaml` | NEW — kro ResourceGraphDefinition (replaces api/v1alpha1 + routing controller) |
| `internal/controller/mongodb_controller.go` | NEW — mock MCK reconciler (unstructured) |
| `internal/controller/flexcluster_controller.go` | NEW — mock Atlas reconciler (unstructured) |
| `cmd/mock-mongodb/main.go` | NEW — manager entrypoint for MongoDB mock |
| `cmd/mock-flexcluster/main.go` | NEW — manager entrypoint for FlexCluster mock |
| `cmd/provisioner/main.go` | NEW — HTTP provisioner server |
| `internal/provisioner/workspace.go` | NEW — workspace + APIBinding creation |
| `cmd/provisioner/static/index.html` | NEW — web UI |
| `deploy/kcp/workspaces.yaml` | NEW — bootstrap dbaas-provider + consumers workspaces |
| `deploy/kro/kro-values.yaml` | NEW — kro Helm values |
| `deploy/mock-mongodb/deployment.yaml` | NEW |
| `deploy/mock-flexcluster/deployment.yaml` | NEW |
| `deploy/provisioner/deployment.yaml` | NEW |
| `Makefile` | UPDATE — revised targets |
| `api/v1alpha1/` | DELETE — no longer needed |
| `internal/controller/mongodatabase_controller.go` | DELETE — kro handles this |
| `cmd/controller/` | DELETE — replaced by focused cmd dirs |

---

## Verification

1. `kubectl get pods -n kcp` — KCP pods Running
2. `kubectl get pods -n kro` — kro pod Running
3. `kubectl get crd | grep mongodb` — MCK, Atlas + `mongodatabases.dbaas.mongodb.com` (from kro) present
4. Open provisioner UI (`kubectl port-forward svc/provisioner 8080`) → provision workspace `tenant-a`
5. Download kubeconfig → `export KUBECONFIG=/tmp/tenant-a.kubeconfig`
6. `kubectl get crd` — only `mongodatabases.dbaas.mongodb.com` visible (via APIBinding)
7. `kubectl apply -f examples/mongodatabase-onpremise.yaml` → status shows `state: Running`
8. `kubectl apply -f examples/mongodatabase-atlas.yaml` (provider: AWS) → status shows `state: IDLE`
9. In physical cluster: `kubectl get mongodb,flexclusters` — child resources created by kro
10. `kubectl get mongodatabase my-db -o jsonpath='{.status}'` — connectionString + state populated
