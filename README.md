# DBaaS — Database as a Service PoC

A proof-of-concept multi-tenant Database-as-a-Service built on [KCP](https://kcp.io), [kro](https://kro.run), and MongoDB's Kubernetes operators.

Consumers interact with a single, simplified `MongoDBDatabase` API. Behind the scenes the platform routes their request to either an on-premise MongoDB cluster (via [MCK](https://github.com/mongodb/mongodb-kubernetes)) or a cloud Atlas cluster (via [Atlas Kubernetes Operator](https://github.com/mongodb/mongodb-atlas-kubernetes)), depending on the `provider` field.

---

## Architecture

```
Physical Kubernetes Cluster (kind / minikube)
│
│  ┌─── cert-manager ───────────────────────────────────────────── ┐
│  │  Manages TLS certificates for KCP (required prerequisite)     │
│  └────────────────────────────────────────────────────────────── ┘
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
│  │  POST /api/workspaces/{n}/delete   → delete workspace         │
│  │  GET  /api/workspaces/{n}/kubeconfig → download kubeconfig    │
│  │  GET  /                            → HTML management UI       │
│  └────────────────────────────────────────────────────────────── ┘
│
│  ┌─── Headlamp (Helm, NodePort 4466) ─────────────────────────── ┐
│  │  Kubernetes GUI scoped per tenant workspace                    │
│  │  Kubeconfig maintained dynamically by the provisioner         │
│  └────────────────────────────────────────────────────────────── ┘
```

### End-to-end flow

1. Tenant opens the provisioner UI, creates a workspace for themselves.
2. Tenant downloads their kubeconfig and applies a `MongoDBDatabase` manifest.
3. The API Sync Agent syncs the resource to the physical cluster.
4. kro's microcontroller creates the appropriate backend resource (`MongoDB` or `FlexCluster`).
5. The mock controller reconciles the backend and writes status.
6. kro propagates status → `MongoDBDatabase.status`.
7. The sync agent pushes status back up to the tenant's KCP workspace.
8. Tenant runs `kubectl get mongodatabase my-db` and sees `state` + `connectionString`.

---

## Resource lifecycle: from tenant to physical cluster

This section traces a single `MongoDBDatabase` through every layer of the system,
using a concrete example: tenant workspace `root:consumers:test` (internal cluster
ID `1agg86w8arvo93ki`) creating a database named `my-onprem-db` with
`provider: ON-PREMISE`.

```
  KCP (virtual clusters)                    Physical Kubernetes cluster
  ══════════════════════                    ═══════════════════════════

  root:consumers:test
  ┌─────────────────────────────┐
  │ MongoDBDatabase             │  ①                ┌─────────────────────────────────────┐
  │  ns:   default              │ ─── sync down ──► │ MongoDBDatabase                     │
  │  name: my-onprem-db         │                   │  ns:   1agg86w8arvo93ki             │
  │  spec:                      │                   │  name: 2747cabb…-cc2e300d…          │
  │    provider: ON-PREMISE     │                   │  annotations:                       │
  │                             │                   │    remote-object-name: my-onprem-db │
  │  status:             ◄──────┼──── sync up ───   │  spec:                              │
  │    state: ACTIVE     ⑥      │                   │    provider: ON-PREMISE             │
  │    connectionString: …      │                   └──────────────┬──────────────────────┘
  └─────────────────────────────┘                                  │ ② kro reconciles
                                                                   │   includeWhen:
                                                                   │   provider==ON-PREMISE
                                                                   ▼
                                                   ┌─────────────────────────────────────┐
                                                   │ mongodb.com/v1 MongoDB      ③        │
                                                   │  ns:   1agg86w8arvo93ki             │
                                                   │  name: 2747cabb…-cc2e300d…          │
                                                   │  spec:                              │
                                                   │    type: ReplicaSet                 │
                                                   │                                     │
                                                   │  status:                     ④      │
                                                   │    phase: Running                   │
                                                   └──────────────┬──────────────────────┘
                                                                  │ ⑤ kro aggregates
                                                                  │   status back to
                                                                  ▼   MongoDBDatabase
                                                   ┌─────────────────────────────────────┐
                                                   │ MongoDBDatabase.status              │
                                                   │  state: ACTIVE                      │
                                                   │  connectionString:                  │
                                                   │    mongodb://2747cabb….svc:27017    │
                                                   └─────────────────────────────────────┘
```

### 1 — Tenant creates MongoDBDatabase in KCP

```
KCP workspace: root:consumers:test  (cluster ID: 1agg86w8arvo93ki)
  namespace:   default
  name:        my-onprem-db
  spec.provider: ON-PREMISE
```

The tenant's kubeconfig points at the KCP front-proxy. From the tenant's
perspective this is a normal `kubectl apply`. The workspace only exposes the
`MongoDBDatabase` CRD (via `APIBinding`) — no other resources are visible.

### 2 — API Sync Agent syncs the object to the physical cluster

The sync agent watches all KCP workspaces that have bound the
`mongodatabases.dbaas.mongodb.com` APIExport. When it sees the new object it
creates a mirror on the physical cluster with a **transformed identity**:

| Field | KCP (tenant view) | Physical cluster |
|---|---|---|
| namespace | `default` | `1agg86w8arvo93ki` (workspace cluster ID) |
| name | `my-onprem-db` | `2747cabbb481a433679f-cc2e300df005cd9a4afb` (hash) |

**Why the namespace changes:** the sync agent creates one namespace per KCP
workspace on the physical cluster, named after the workspace's internal cluster
ID. This keeps every tenant's objects isolated without requiring any namespace
pre-provisioning.

**Why the name changes:** the sync agent hashes the combination of workspace
cluster ID + original namespace + original name into a deterministic, fixed-length
name. This prevents collisions when multiple tenants each create a
`MongoDBDatabase/default/my-db`.

The original coordinates are preserved as annotations on the physical object so
nothing is lost:

```yaml
annotations:
  syncagent.kcp.io/remote-object-cluster:   1agg86w8arvo93ki
  syncagent.kcp.io/remote-object-namespace: default
  syncagent.kcp.io/remote-object-name:      my-onprem-db
```

### 3 — kro reconciles MongoDBDatabase → creates child resource

kro's microcontroller watches `MongoDBDatabase` objects on the physical cluster.
It evaluates `includeWhen` on each resource in the RGD:

- `schema.spec.provider == "ON-PREMISE"` → `mckMongoDB` included
- `schema.spec.provider == "AWS" || schema.spec.provider == "AZURE"` → `atlasFlexCluster` excluded

kro creates the `mongodb.com/v1 MongoDB` child using `schema.metadata.name` and
`schema.metadata.namespace` from the physical object — i.e. the hashed values:

```
namespace: 1agg86w8arvo93ki
name:      2747cabbb481a433679f-cc2e300df005cd9a4afb
```

### 4 — Mock controller reconciles MongoDB → writes status

The mock MongoDB controller reconciles the `mongodb.com/v1 MongoDB` object and
sets:

```yaml
status:
  phase: Running
```

### 5 — kro aggregates status → MongoDBDatabase.status

kro reads `mckMongoDB.status.phase` and writes it back to the `MongoDBDatabase`
status, also deriving the `connectionString` from the physical name and namespace:

```yaml
status:
  state: ACTIVE
  connectionString: mongodb://2747cabbb481a433679f-cc2e300df005cd9a4afb.1agg86w8arvo93ki.svc:27017
  conditions:
    - type: Ready
      status: "True"
```

### 6 — Sync agent pushes status back to the KCP workspace

The sync agent watches for status changes on the physical object and writes them
back to the original `my-onprem-db` object in the tenant's KCP workspace. The
tenant sees:

```
$ kubectl --kubeconfig test.kubeconfig get mongodbdatabase my-onprem-db
NAME           PROVIDER     STATE    READY
my-onprem-db   ON-PREMISE   ACTIVE   True
```

The tenant never sees the hashed name or the physical cluster namespace — those
are an internal implementation detail of the sync layer.

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
│   ├── kind/               # kind cluster config (extraPortMappings)
│   ├── kcp/                # KCP Helm values + workspace bootstrap manifest
│   ├── kro/                # kro Helm values
│   ├── headlamp/           # Headlamp Helm values + kubeconfig Secret + RBAC
│   ├── mock-mongodb/       # RBAC + Deployment
│   ├── mock-flexcluster/   # RBAC + Deployment
│   └── provisioner/        # Deployment + Service
├── scripts/
│   └── deploy.py           # pipeline deploy UI (spinner + status bar)
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
| [helm](https://helm.sh/) ≥ 3.14 | install cert-manager, KCP, kro, API Sync Agent |
| [ko](https://ko.build/) | build + load Go container images |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | interact with cluster and KCP |
| Go ≥ 1.22 | build locally |

cert-manager is installed automatically by `make deploy-kcp` — no manual step needed.

---

## Deploy

### 1 — Deploy everything

```bash
make deploy
```

This runs the full pipeline in one shot, including creating the kind cluster:

```
kind → helm-repos → cert-manager → kcp → crds → kro → kubeconfig → bootstrap → sync-agent → provisioner → controllers → headlamp
```

A status bar at the bottom of the terminal tracks progress with a spinner and
shows each stage turning green as it completes.

**Ordering constraints enforced by the pipeline:**
- cert-manager must be ready before KCP — KCP uses cert-manager `Certificate` and `Issuer` resources for its entire TLS PKI.
- kro must complete before the sync agent — kro dynamically creates the `MongoDBDatabase` CRD; the sync agent needs it to exist at startup.
- KCP workspace bootstrap runs as a Kubernetes Job inside the cluster (against `kcp-front-proxy.kcp.svc.cluster.local:8443`), so no local port-forward is needed.

The kind cluster is configured with host port mappings, so no port-forwarding is needed at any point:

| Service | Host URL |
|---|---|
| KCP front-proxy | `https://localhost:6443` |
| Provisioner UI | `http://localhost:8090` |
| Headlamp GUI | `http://localhost:4466` |

Open **http://localhost:8090** in your browser once the pipeline completes.

To tear everything down including the kind cluster:

```bash
make undeploy
make kind-delete
```

---

## Usage

### Provision a consumer workspace

Tenants self-service their own workspace via the provisioner UI:

1. Open http://localhost:8090.
2. Enter a workspace name (e.g. `tenant-a`) and click **Provision**.
3. Once the workspace is `Ready`, click **↓ kubeconfig** to download, or **↗ Headlamp** to open the workspace directly in the Headlamp Kubernetes GUI.

The provisioner creates `root:consumers:tenant-a` in KCP, binds the `mongodatabases.dbaas.mongodb.com` APIExport from `root:dbaas-provider`, and returns a kubeconfig scoped to that workspace. From inside the workspace the only visible CRD is `MongoDBDatabase`.

### Create a database

```bash
export KUBECONFIG=/path/to/tenant-a.kubeconfig

# on-premise MongoDB (creates the MCK MongoDB child resource)
kubectl apply -f - <<EOF
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

# cloud Atlas FlexCluster (creates the Atlas FlexCluster child resource)
kubectl apply -f - <<EOF
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

> **Note:** `apiVersion` is `kro.run/v1alpha1` — kro v0.9.0 always creates CRDs
> in the `kro.run` group. See [kro limitations](#kro-v090-known-limitations).

### Inspect status

```bash
kubectl get mongodbdatabases
# NAME           PROVIDER     STATE    READY
# my-onprem-db   ON-PREMISE   ACTIVE   True
# my-atlas-db    AWS                   False

kubectl get mongodatabase my-onprem-db -o jsonpath='{.status}'
```

### Inspect backend resources (physical cluster)

```bash
# switch back to the physical cluster kubeconfig
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

## kro v0.9.0 known limitations

These limitations were discovered during deployment and affect the PoC design:

**CRD group is always `kro.run`**
kro v0.9.0 derives the CRD group from the Kind name and always places generated
CRDs in the `kro.run` group. The RGD `metadata.name` (e.g.
`mongodatabases.dbaas.mongodb.com`) is only the name of the RGD object — it does
not control the group of the CRD kro creates. The actual CRD is
`mongodbdatabases.kro.run`. The sync agent `PublishedResource` and any RBAC must
reference `kro.run`, not `dbaas.mongodb.com`.

**Schema fields: only `type` is supported**
kro's schema parser only accepts `type` for scalar fields. Standard OpenAPI
keywords such as `default`, `enum`, and `description` are not recognised and
cause a parse error (`unexpected type: <value>`). Remove them from
`spec.schema.spec.*` fields.

**`schema.spec.*` in `includeWhen`: requires a `default` value**
String equality in `includeWhen` (e.g. `${schema.spec.provider == "ON-PREMISE"}`)
works in kro v0.6.3+. The field must have a `default` value in the schema;
without one kro's static evaluator throws `no such key` at validation time.
kro's SimpleSchema does not support the `default` keyword for scalar fields
(only `type` is accepted), so ensure the field is always set by the caller.
A `type kind mismatch` error on `schema.spec.*` fields indicates you are running
kro < v0.6.3 — upgrade to v0.9.0+.

**Status CEL is still asymmetric across branches**
The current graph status block only copies data from the `mckMongoDB` branch.
That keeps the on-prem path useful, but it means the Atlas branch does not yet
surface equivalent `state` and `connectionString` fields through
`MongoDBDatabase.status`. The Atlas child object is still created and reconciled;
the limitation is in what the graph currently exposes back on the top-level API.

---

## TLS trust relationships

KCP's PKI is managed entirely by cert-manager. The diagram below shows every signing and trust relationship; the numbered notes explain the non-obvious parts.

```
 CERTIFICATE AUTHORITIES (cert-manager, self-signed, namespace: kcp)
 ┌─────────────────────────────────────────────────────────────────────────────┐
 │                                                                             │
 │  kcp-ca  ─────────────── signs ──────────►  front-proxy serving cert  (1) │
 │                                              SANs: localhost                │
 │                                                    kcp-front-proxy          │
 │                                                    .kcp.svc.cluster.local   │
 │                                                                             │
 │  kcp-client-ca                                     (KCP-internal use)       │
 │                                                                             │
 │  kcp-front-proxy-client-ca  ── signs ──►  kcp-admin-client-cert       (2) │
 │                                            CN = kcp-admin                   │
 │                                            O  = system:kcp:admin            │
 │                                            usage: client auth               │
 └─────────────────────────────────────────────────────────────────────────────┘

 TRUST ANCHORS  (--client-ca-file accepted by each server)
 ┌─────────────────────────────────────────────────────────────────────────────┐
 │                                                                             │
 │  KCP front-proxy  (:8443)   trusts  kcp-front-proxy-client-ca              │
 │                                                                             │
 │  KCP API server   (:6443)   trusts  kcp-combined-client-ca  ◄── patched (3)│
 │                                       ├── kcp-client-ca                     │
 │                                       └── kcp-front-proxy-client-ca         │
 │                                                                             │
 └─────────────────────────────────────────────────────────────────────────────┘

 CLIENT CONNECTIONS
 ┌─────────────────────────────────────────────────────────────────────────────┐
 │                                                                             │
 │  host (admin / provisioner)                                                 │
 │    server  https://localhost:6443  (via kind NodePort)                      │
 │    CA      kcp-ca                  (verifies serving cert)            (1)  │
 │    cert    kcp-admin-client-cert   (authenticates caller)             (2)  │
 │                │                                                            │
 │                └──────────────────────────────────► KCP front-proxy :8443  │
 │                                                                             │
 │  sync agent pod / provisioner pod                                           │
 │    server  https://kcp-front-proxy.kcp.svc.cluster.local:8443        (4)  │
 │    CA      kcp-ca                  (SAN in serving cert — same CA)    (1)  │
 │    cert    kcp-admin-client-cert   (same cert, no skip-TLS needed)    (2)  │
 │                │                                                            │
 │                └──────────────────────────────────► KCP front-proxy :8443  │
 │                                                                             │
 └─────────────────────────────────────────────────────────────────────────────┘
```

**(1) Front-proxy serving cert SANs** — `kcp-values.yaml` adds
`kcp-front-proxy.kcp.svc.cluster.local` via `extraDNSNames` so that both the
host (through the kind NodePort) and in-cluster pods can verify the same serving
cert with the same `kcp-ca`, without `insecureSkipTLSVerify`.

**(2) Admin client certificate** — `deploy/kcp/admin-cert.yaml` requests a
cert-manager `Certificate` issued by `kcp-front-proxy-client-issuer` (backed by
`kcp-front-proxy-client-ca`). The resulting secret `kcp-admin-client-cert` is
embedded into every kubeconfig. `make get-kcp-kubeconfig` assembles the
self-contained kubeconfig at `/tmp/kcp-admin.kubeconfig` by inlining the
`kcp-ca` cert and the `kcp-admin-client-cert` cert+key directly as base64 data
fields.

**(3) Combined CA patch** — The KCP workspace controller authenticates directly
to the KCP API server (`kcp:6443/services/initializingworkspaces`) using a cert
signed by `kcp-front-proxy-client-ca`. By default, `kcp:6443` only trusts
`kcp-client-ca`, so the controller is rejected with `401 Unauthorized` and all
workspaces stay stuck in `Initializing`. `make deploy-kcp` runs
`patch-kcp-client-ca` to fix this: it concatenates both CA PEM blocks into a
new secret `kcp-combined-client-ca` and patches the KCP `Deployment` to mount
it as `--client-ca-file`.

**(4) In-cluster kubeconfigs** — Pods cannot reach `localhost:6443`. The sync
agent and provisioner each receive a copy of the admin kubeconfig with the
server URL rewritten to the in-cluster front-proxy address. TLS verification
requires no changes because the serving cert already carries the in-cluster
hostname as a SAN (see note 1).

---

## Workspace deletion and cascade behaviour

Deleting a workspace via the provisioner UI (or `DELETE /api/workspaces/{name}`)
issues a single `DELETE` on the `tenancy.kcp.io/v1alpha1 Workspace` object. KCP
garbage-collects everything that lives inside the workspace, so this has the
following cascade effect:

- The `APIBinding` (`dbaas`) inside the workspace is deleted.
- All `MongoDBDatabase` resources the tenant created inside the workspace are deleted.

**Atlas cluster orphan risk**

The sync agent mirrors each `MongoDBDatabase` to the physical cluster, and kro
creates a child `FlexCluster` (or `MongoDB`) from it. The mock controllers
reconcile those children and set status, but they do not own any real cloud
infrastructure in this PoC. In a production system where the controller
provisions a real Atlas cluster, workspace deletion could orphan the cloud
resource:

1. KCP deletes the `MongoDBDatabase` in the tenant workspace.
2. The sync agent propagates the deletion to the physical cluster.
3. The controller must handle the `MongoDBDatabase` deletion (finalizer) and
   deprovision the Atlas cluster **before** it is removed.

If KCP tears down the workspace faster than the controller can reconcile the
deletion, or if the controller crashes during that window, the Atlas cluster is
left running and billing without any Kubernetes object tracking it.

Mitigation in a production deployment: add a finalizer to every `MongoDBDatabase`
managed by a real controller, and ensure the workspace deletion waits for the
finalizer to be removed (i.e. the controller deprovisions the cloud resource
first).

---

## Design notes

See [`dbaas.md`](dbaas.md) for the full design document, including:

- Why kro over Crossplane
- KCP workspace hierarchy rationale (Universal vs custom WorkspaceType)
- Provisioner auth — dev token vs production OIDC
- Sync agent startup ordering constraint
