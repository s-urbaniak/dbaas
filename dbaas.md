# DBaaS Architecture and Operations

For setup and day-one usage, start with [README.md](README.md). This document
is the canonical
architecture and operations reference for the repository as it exists today.

## Overview

This repository implements a local multi-tenant DBaaS demo on top of kcp,
kro, Cluster API, and a Kubernetes cluster.

The high-level model is:

- kcp provides tenant workspaces.
- Cluster API + CAPD run in the physical cluster as local management-plane
  infrastructure for tenant workload clusters.
- kro defines two tenant-facing APIs: `MongoDBDatabase` and `Kubernetes`.
- Two API Sync Agent deployments export those APIs from the provider workspace
  and sync instances between kcp and the physical cluster.
- Mock controllers reconcile MongoDB resources, and the
  `kubernetes-controller` reconciles tenant Kubernetes clusters and mounts.
- A small provisioner creates consumer workspaces and maintains Headlamp
  access to them.

## Topology

```text
Physical Kubernetes cluster
|
|-- cert-manager
|   `-- issues kcp serving and client certificates
|
|-- Cluster API management stack
|   |-- cluster-api core provider
|   |-- kubeadm bootstrap provider
|   |-- kubeadm control-plane provider
|   `-- CAPD (Docker infrastructure provider)
|
|-- kcp
|   |-- root:dbaas-provider
|   |   |-- APIExport/dbaas.mongodb.com
|   |   `-- APIExport/kubernetes.dbaas.mongodb.com
|   |
|   `-- root:consumers
|       `-- root:consumers:<tenant>
|           |-- APIBinding/dbaas
|           |-- APIBinding/kubernetes
|           |-- MongoDBDatabase objects
|           |-- Kubernetes objects
|           `-- mounted child workspaces for provisioned tenant clusters
|
|-- API Sync Agents
|   |-- api-syncagent-mongodb
|   `-- api-syncagent-kubernetes
|
|-- kro
|   |-- ResourceGraphDefinition for MongoDBDatabase
|   `-- ResourceGraphDefinition for Kubernetes
|
|-- mock-mongodb controller
|   `-- reconciles mongodb.com/v1 MongoDB
|
|-- mock-flexcluster controller
|   `-- reconciles atlas.generated.mongodb.com/v1 FlexCluster
|
|-- provisioner
|   `-- creates and deletes consumer workspaces and kubeconfigs
|
|-- kubernetes-controller
|   |-- manages Kubernetes status and deletion
|   `-- exposes mounted CAPD clusters back to kcp
|
`-- Headlamp
    |-- one shared deployment
    |-- workspace kubeconfig maintained by the provisioner
    `-- kcp plugin for Workspaces and API Bindings, including Instances view
```

## End-to-End Flow

1. A tenant opens the provisioner UI and creates a workspace.
2. The provisioner creates `root:consumers:<tenant>`.
3. The provisioner creates `APIBinding/dbaas` in that workspace.
4. The provisioner creates a workspace-local service account, token secret,
   and `ClusterRoleBinding` for newly provisioned workspaces.
5. The tenant downloads a kubeconfig or opens the workspace in Headlamp.
6. The tenant creates a `MongoDBDatabase` or `Kubernetes` object.
7. The corresponding API Sync Agent mirrors the object into the physical
   cluster.
8. kro creates the backing resources.
9. The corresponding controller writes backend status.
10. kro or the `kubernetes-controller` updates the tenant-facing status.
11. The API Sync Agent syncs that status back to the tenant workspace.

## MongoDB Resource Lifecycle

This section traces a single `MongoDBDatabase` through every layer of the
system, using tenant workspace `root:consumers:test` and a database named
`my-onprem-db` with `provider: ON-PREMISE`.

```text
  kcp (virtual clusters)                    Physical Kubernetes cluster
  ======================                    ===========================

  root:consumers:test
  +-----------------------------+
  | MongoDBDatabase             |  1                +------------------+
  |  ns:   default              | ---- sync down -->| MongoDBDatabase  |
  |  name: my-onprem-db         |                   |  ns: 1agg...     |
  |  spec.provider: ON-PREMISE  |                   |  name: 2747...   |
  |  status:             <------+---- sync up ----- |                  |
  +-----------------------------+                   +--------+---------+
                                                             | 2 kro
                                                             v
                                               +---------------------------+
                                               | mongodb.com/v1 MongoDB    |
                                               |  ns:   1agg...            |
                                               |  name: 2747...            |
                                               |  status.phase: Running    |
                                               +------------+--------------+
                                                            | 3 aggregate
                                                            v
                                               +---------------------------+
                                               | MongoDBDatabase.status    |
                                               |  state: ACTIVE            |
                                               |  connectionString: ...    |
                                               +---------------------------+
```

### 1. Tenant creates MongoDBDatabase in kcp

The tenant's kubeconfig points at the kcp front-proxy. From the tenant's
perspective this is a normal `kubectl apply`.

For newly provisioned workspaces, that kubeconfig is generated from a
workspace-local service account token rather than the kcp admin credentials.

### 2. API Sync Agent syncs the object to the physical cluster

The sync agent watches kcp workspaces that have bound the exported API and
creates a mirror on the physical cluster with a transformed identity.

| Field | kcp tenant view | Physical cluster |
|---|---|---|
| namespace | `default` | internal workspace cluster ID |
| name | `my-onprem-db` | deterministic hash |

Why the namespace changes:
- the sync agent creates one namespace per kcp workspace on the physical
  cluster
- that keeps tenant objects isolated without pre-provisioning namespaces

Why the name changes:
- the sync agent hashes workspace cluster ID + original namespace + original
  name
- that avoids collisions when different tenants create the same object name

The original coordinates remain on the physical object as annotations.

### 3. kro reconciles MongoDBDatabase into a child resource

The `ResourceGraphDefinition` in `config/kro/mongodatabase-rgd.yaml` uses
`includeWhen` on `spec.provider`:

- `ON-PREMISE` creates `mongodb.com/v1 MongoDB`
- `AWS` or `AZURE` creates `atlas.generated.mongodb.com/v1 FlexCluster`

kro uses the mirrored physical name and namespace for the child resource.

### 4. Mock controller writes backend status

The mock MongoDB controller writes `status.phase=Running` on
`mongodb.com/v1 MongoDB`.

The mock FlexCluster controller writes status on
`atlas.generated.mongodb.com/v1 FlexCluster`.

### 5. kro aggregates backend status

kro writes derived status onto `MongoDBDatabase.status`, including:

- `state`
- `connectionString`
- `Ready` condition

The current graph mainly reflects the on-prem branch in this top-level status.

### 6. Sync agent pushes status back to the tenant workspace

The tenant sees backend state on the original object and does not see the
hashed physical name or the physical-cluster namespace.

## Kubernetes Resource Lifecycle

This section traces a tenant `Kubernetes` object through every layer of the
system, using tenant workspace `root:consumers:test` and a cluster named
`demo-cluster`.

1. The tenant creates `kro.run/v1alpha1 Kubernetes` in `root:consumers:test`.
2. `api-syncagent-kubernetes` mirrors it into the physical cluster using the
   service-cluster namespace for that tenant workspace and a hashed object
   name.
3. kro expands `config/kro/kubernetes-rgd.yaml` into the required CAPI/CAPD
   resources:
   - `Cluster`
   - `DockerCluster`
   - `KubeadmControlPlane`
   - `DockerMachineTemplate`
   - `KubeadmConfigTemplate`
   - `MachineDeployment`
4. The generated `Cluster` is labeled for the Calico `ClusterResourceSet`, so
   the workload cluster gets a CNI automatically.
5. The `kubernetes-controller` watches the synced `Kubernetes` object, waits
   for the workload cluster kubeconfig to appear, ensures Calico is installed,
   and reconciles control-plane taints according to
   `spec.allowSchedulingOnControlPlanes`.
6. Once the workload cluster is ready, the controller publishes
   `status.URL`, marks the resource ready, and creates a mounted child
   workspace under the tenant workspace.
7. The tenant can then enter that child workspace and use the provisioned
   CAPD cluster through kcp workspace mounts.

Deleting the tenant `Kubernetes` object triggers the reverse path:

1. sync-agent starts background cleanup on the service-cluster object.
2. the `kubernetes-controller` finalizer deletes the mounted child workspace.
3. once the finalizer is removed, KRO deletes the generated CAPI objects.
4. CAPI and CAPD tear down the Docker-backed workload cluster.

## Major Components

### kcp

kcp hosts two important root-level workspaces:

- `root:dbaas-provider`
- `root:consumers`

`root:dbaas-provider` is the service-provider workspace. The API Sync Agents
publish the `MongoDBDatabase` and `Kubernetes` APIs from there.

`root:consumers` is the parent for tenant workspaces. The provisioner creates
child workspaces under it and adds the `dbaas` and `kubernetes` APIBindings
inside each one.

### kro

kro owns the `ResourceGraphDefinition`s in:

- `config/kro/mongodatabase-rgd.yaml`
- `config/kro/kubernetes-rgd.yaml`

Today the generated tenant APIs are:

- `kro.run/v1alpha1 MongoDBDatabase`
- `kro.run/v1alpha1 Kubernetes`

The MongoDB graph branches on `spec.provider` and currently exposes richer
top-level status for the on-prem path than for the Atlas path.

The Kubernetes graph exposes:

- `spec.machineCount.controlPlane`
- `spec.machineCount.worker`
- `spec.allowSchedulingOnControlPlanes`

and synthesizes the required CAPD-backed CAPI resources for a tenant cluster.

### API Sync Agent

The API Sync Agent runs as two deployments:

- `api-syncagent-mongodb`
- `api-syncagent-kubernetes`

Together they export the generated `MongoDBDatabase` and `Kubernetes` APIs
from `root:dbaas-provider` and synchronize instances and status between kcp
and the physical cluster.

Deploy order matters here:

- MCK and Atlas CRDs must exist before kro applies the graph.
- kro must apply the graphs before the sync agents start.
- the sync agents must see `mongodbdatabases.kro.run` and
  `kubernetes.kro.run` at startup.

### Mock controllers

The mock controllers live in:

- `internal/controller/mongodb_controller.go`
- `internal/controller/flexcluster_controller.go`

They simulate backend behavior rather than provisioning real infrastructure.

### Cluster API + CAPD

The physical kind cluster also runs Cluster API provider components:

- `cluster-api`
- `kubeadm bootstrap`
- `kubeadm control-plane`
- `docker` infrastructure provider (CAPD)
- a `ClusterResourceSet` that applies Calico to repo-created workload clusters

This repository now uses them for both:

- the local management cluster bootstrap
- tenant-facing `Kubernetes` resources provisioned through kcp

CAPD also requires higher host inotify limits on Linux. The repo now checks
for the recommended values before creating the kind management cluster,
bootstrapping CAPD, or creating a workload cluster.

### Kubernetes Controller

The Kubernetes controller lives in:

- `cmd/kubernetes-controller`
- `internal/controller/kubernetes_controller.go`

It is responsible for:

- reflecting workload-cluster readiness into `Kubernetes.status`
- ensuring Calico is installed in the workload cluster
- reconciling whether control-plane nodes keep or drop the standard
  `NoSchedule` taint
- creating and deleting the mounted child workspaces
- serving the reverse-proxy endpoint that kcp workspace mounts target

### Provisioner

The provisioner is a small Go HTTP server in
[main.go](cmd/provisioner/main.go) backed by workspace logic in
[workspace.go](internal/provisioner/workspace.go).

It uses the published kcp SDK clientset directly for kcp-native resources:

- `tenancy.kcp.io/v1alpha1 Workspace`
- `apis.kcp.io/v1alpha1 APIBinding`
- `core.kcp.io/v1alpha1 LogicalCluster`

It still uses the dynamic client only for counting
`mongodbdatabases.kro.run` objects in tenant workspaces.

Provisioner endpoints:

- `GET /`
- `POST /api/workspaces`
- `GET /api/workspaces/{name}/kubeconfig`
- `POST /api/workspaces/{name}/delete`

Provisioner responsibilities:

- create a consumer workspace under `root:consumers`
- wait for the workspace to become ready and expose `Spec.URL`
- create the `dbaas` and `kubernetes` APIBindings in that workspace
- create a workspace-local service account, token secret, and
  `ClusterRoleBinding` for new workspaces
- generate tenant kubeconfigs
- delete consumer workspaces
- derive better deletion state from the child `LogicalCluster`
- reconcile missing APIBindings across existing workspaces
- reconcile the Headlamp workspace kubeconfig

The provisioner process is signal-aware and shuts down from a root context
created with `signal.NotifyContext`, so in-cluster `SIGTERM` and local
`Ctrl-C` both stop the server and periodic reconcile loops cleanly.

### Headlamp

Headlamp is deployed from `deploy/headlamp` and receives:

- a shared kubeconfig secret managed by the provisioner
- a plugin bundle built from `headlamp-plugin/kcp`
- RBAC for reading workspaces and managing the kubeconfig secret

The provisioner keeps the `headlamp-workspace-kubeconfig` secret aligned with
the current set of non-terminating consumer workspaces and restarts the
Headlamp deployment when that secret changes.

For newly provisioned workspaces, each Headlamp context now uses a
workspace-local service account token instead of the provisioner admin
credentials. Existing workspaces keep their older admin-derived contexts until
they are migrated.

The kcp Headlamp plugin currently provides:

- a Workspaces view
- an API Bindings view
- an API Binding Instances view
- right-side instance details with an edit action

## Authentication and Access Model

There are three credential classes in the current system.

Cluster-side components:
- the provisioner and sync agent use kcp admin-style kubeconfigs against the
  in-cluster front-proxy endpoint
- these kubeconfigs are required for cluster bootstrap and reconciliation

New tenant workspaces:
- the provisioner creates a workspace-local service account in `default`
- it creates a token secret for that service account
- it binds that service account to `cluster-admin` inside the tenant workspace
- the downloaded kubeconfig and Headlamp context for that workspace are built
  from that token

Existing tenant workspaces:
- older workspaces can still have admin-derived kubeconfigs and Headlamp
  contexts until they are migrated

Practical limitation:
- the workspace-local service-account kubeconfig is intended for the normal
  tenant flow
- it removes the admin certificate from new tenant credentials
- it should not be treated as a hard per-URL identity binding across all kcp
  paths

## TLS Trust Relationships

kcp's PKI is managed by cert-manager. The important trust relationships are:

- `kcp-ca` signs the front-proxy serving cert
- `kcp-front-proxy-client-ca` signs the admin client certificate
- the kcp front-proxy trusts the front-proxy client CA
- the kcp workspace controller reaches the front-proxy through the in-cluster
  Service using a dedicated external-logical-cluster-admin kubeconfig

Operationally important details:

- the front-proxy serving cert includes `localhost`, the in-cluster Service
  DNS name, and any extra SANs supplied through
  `KCP_FRONT_PROXY_EXTRA_SANS`
- `/tmp/kcp-admin.kubeconfig` is materialized with inline cert data
- in-cluster kubeconfigs point at
  `https://kcp-front-proxy.kcp.svc.cluster.local:8443`
- host-facing kubeconfigs rendered by the provisioner reuse the hostname or IP
  used to open the UI and keep port `6443`

## Workspace Deletion Semantics

Deleting a tenant workspace from the provisioner sends a delete request for the
parent `Workspace` object in `root:consumers`.

kcp then deletes:

- the `APIBinding` inside the workspace
- the `MongoDBDatabase` and `Kubernetes` objects inside the workspace
- the child `LogicalCluster`

The parent `Workspace` object can remain visible briefly after the child
`LogicalCluster` is already gone. The provisioner UI therefore derives a more
useful status from the child `LogicalCluster`:

- `Deleting content`
- `Finalizing parent`

That reflects real deletion progress more accurately than the raw workspace
phase alone.

Implementation note:
- tenant `Kubernetes` objects already use a finalizer-driven cleanup path via
  the `kubernetes-controller` before the mounted child workspace and CAPD
  cluster disappear

## Deploy Flow

The main entrypoint is:

```bash
make deploy
```

The deploy pipeline is:

```text
kind -> helm-repos -> cert-manager -> capi -> kcp -> crds -> kro -> kubeconfig
-> bootstrap -> sync-agent -> provisioner -> controllers -> headlamp
```

Important deploy details:

- kcp depends on cert-manager for serving and client certificates.
- kcp mounts a repo-managed kubeconfig so the workspace controller reaches the
  front-proxy via `https://kcp-front-proxy.kcp.svc.cluster.local:8443`.
- the kcp admin kubeconfig is materialized into `/tmp/kcp-admin.kubeconfig`
  with inline cert data
- bootstrap of `root:dbaas-provider` and `root:consumers` runs as an in-cluster
  Job
- the provisioner and sync agent receive in-cluster kubeconfigs that point at
  `https://kcp-front-proxy.kcp.svc.cluster.local:8443`
- Headlamp deploy includes the plugin bundle and a kubeconfig bootstrap step

## Known Constraints

- Newly provisioned tenant kubeconfigs and Headlamp contexts now use a
  workspace-local service account token instead of the admin credentials.
- Existing workspaces still keep their older admin-derived credentials until
  they are migrated.
- The generated tenant CRD is in the `kro.run` API group, not
  `dbaas.mongodb.com`.
- KRO 0.9.1 currently does not surface RGD field descriptions in generated
  CRDs, so `kubectl explain` still lacks those doc strings.
- The current graph status section mainly reflects the on-prem branch.
- Headlamp workspace access is centralized through one shared deployment and a
  dynamically maintained kubeconfig secret, not through per-user identities.

## Important Paths

Key paths in the current repository:

- [Makefile](Makefile)
- `config/kro/mongodatabase-rgd.yaml`
- `config/kro/kubernetes-rgd.yaml`
- `internal/controller/kubernetes_controller.go`
- [main.go](cmd/provisioner/main.go)
- [index.html](cmd/provisioner/static/index.html)
- [workspace.go](internal/provisioner/workspace.go)
- [index.tsx](headlamp-plugin/kcp/src/index.tsx)
- `headlamp-plugin/kcp/src/APIBindingsPage.tsx`
- `headlamp-plugin/kcp/src/APIBindingInstancesPage.tsx`
