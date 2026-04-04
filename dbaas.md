# DBaaS Architecture and Operations

For setup and day-one usage, start with [README.md](README.md). This document
is the canonical
architecture and operations reference for the repository as it exists today.

## Overview

This repository implements a local multi-tenant DBaaS demo on top of KCP,
kro, Cluster API, and a Kubernetes cluster.

The high-level model is:

- KCP provides tenant workspaces.
- Cluster API + CAPD run in the physical cluster as local management-plane
  infrastructure for Kubernetes workload-cluster experiments.
- kro defines one tenant-facing API: `MongoDBDatabase`.
- The API Sync Agent exports that API from the provider workspace and syncs
  instances between KCP and the physical cluster.
- Mock controllers reconcile the generated backend resources.
- A small provisioner creates consumer workspaces and maintains Headlamp
  access to them.

## Topology

```text
Physical Kubernetes cluster
|
|-- cert-manager
|   `-- issues KCP serving and client certificates
|
|-- Cluster API management stack
|   |-- cluster-api core provider
|   |-- kubeadm bootstrap provider
|   |-- kubeadm control-plane provider
|   `-- CAPD (Docker infrastructure provider)
|
|-- KCP
|   |-- root:dbaas-provider
|   |   `-- APIExport published by the API Sync Agent
|   |
|   `-- root:consumers
|       `-- root:consumers:<tenant>
|           |-- APIBinding/dbaas
|           `-- MongoDBDatabase objects
|
|-- API Sync Agent
|   `-- syncs MongoDBDatabase instances and status between KCP and the cluster
|
|-- kro
|   `-- ResourceGraphDefinition for MongoDBDatabase
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
`-- Headlamp
    |-- one shared deployment
    |-- workspace kubeconfig maintained by the provisioner
    `-- KCP plugin for Workspaces and API Bindings, including Instances view
```

## End-to-End Flow

1. A tenant opens the provisioner UI and creates a workspace.
2. The provisioner creates `root:consumers:<tenant>`.
3. The provisioner creates `APIBinding/dbaas` in that workspace.
4. The provisioner creates a workspace-local service account, token secret,
   and `ClusterRoleBinding` for newly provisioned workspaces.
5. The tenant downloads a kubeconfig or opens the workspace in Headlamp.
6. The tenant creates a `MongoDBDatabase` object.
7. The API Sync Agent mirrors the object into the physical cluster.
8. kro creates either a `MongoDB` or `FlexCluster` child resource.
9. The corresponding mock controller writes backend status.
10. kro updates `MongoDBDatabase.status`.
11. The API Sync Agent syncs that status back to the tenant workspace.

## Resource Lifecycle

This section traces a single `MongoDBDatabase` through every layer of the
system, using tenant workspace `root:consumers:test` and a database named
`my-onprem-db` with `provider: ON-PREMISE`.

```text
  KCP (virtual clusters)                    Physical Kubernetes cluster
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

### 1. Tenant creates MongoDBDatabase in KCP

The tenant's kubeconfig points at the KCP front-proxy. From the tenant's
perspective this is a normal `kubectl apply`.

For newly provisioned workspaces, that kubeconfig is generated from a
workspace-local service account token rather than the KCP admin credentials.

### 2. API Sync Agent syncs the object to the physical cluster

The sync agent watches KCP workspaces that have bound the exported API and
creates a mirror on the physical cluster with a transformed identity.

| Field | KCP tenant view | Physical cluster |
|---|---|---|
| namespace | `default` | internal workspace cluster ID |
| name | `my-onprem-db` | deterministic hash |

Why the namespace changes:
- the sync agent creates one namespace per KCP workspace on the physical
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

## Major Components

### KCP

KCP hosts two important root-level workspaces:

- `root:dbaas-provider`
- `root:consumers`

`root:dbaas-provider` is the service-provider workspace. The API Sync Agent
publishes the `MongoDBDatabase` API from there.

`root:consumers` is the parent for tenant workspaces. The provisioner creates
child workspaces under it and adds the `dbaas` APIBinding inside each one.

### kro

kro owns the `ResourceGraphDefinition` in
`config/kro/mongodatabase-rgd.yaml`.

Today the generated API is:

- group `kro.run`
- version `v1alpha1`
- kind `MongoDBDatabase`
- resource `mongodbdatabases`

The graph branches on `spec.provider` and currently exposes richer top-level
status for the on-prem path than for the Atlas path.

### API Sync Agent

The API Sync Agent exports the generated `MongoDBDatabase` API from
`root:dbaas-provider` and synchronizes instances and status between KCP and the
physical cluster.

Deploy order matters here:

- MCK and Atlas CRDs must exist before kro applies the graph.
- kro must apply the graph before the sync agent starts.
- the sync agent must see `mongodbdatabases.kro.run` at startup.

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

This repository currently uses them only as locally bootstrapped management
infrastructure. The main DBaaS flow does not yet create or manage workload
clusters through CAPI, and no tenant-facing `KubernetesCluster` API is exposed
through KCP in this phase.

CAPD also requires higher host inotify limits on Linux. The repo now checks
for the recommended values before creating the kind management cluster,
bootstrapping CAPD, or creating a workload cluster.

### Provisioner

The provisioner is a small Go HTTP server in
[main.go](cmd/provisioner/main.go) backed by workspace logic in
[workspace.go](internal/provisioner/workspace.go).

It uses the published KCP SDK clientset directly for KCP-native resources:

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
- create the `dbaas` APIBinding in that workspace
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

The KCP Headlamp plugin currently provides:

- a Workspaces view
- an API Bindings view
- an API Binding Instances view
- right-side instance details with an edit action

## Authentication and Access Model

There are three credential classes in the current system.

Cluster-side components:
- the provisioner and sync agent use KCP admin-style kubeconfigs against the
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
- it should not be treated as a hard per-URL identity binding across all KCP
  paths

## TLS Trust Relationships

KCP's PKI is managed by cert-manager. The important trust relationships are:

- `kcp-ca` signs the front-proxy serving cert
- `kcp-front-proxy-client-ca` signs the admin client certificate
- the KCP front-proxy trusts the front-proxy client CA
- the KCP API server is patched to trust a combined client CA so workspace
  initialization callbacks succeed

Operationally important details:

- the front-proxy serving cert includes both localhost and in-cluster DNS names
- `/tmp/kcp-admin.kubeconfig` is materialized with inline cert data
- in-cluster kubeconfigs point at
  `https://kcp-front-proxy.kcp.svc.cluster.local:8443`
- the combined client-CA patch is required so internal KCP controllers can
  authenticate to the API server during workspace initialization

## Workspace Deletion Semantics

Deleting a tenant workspace from the provisioner sends a delete request for the
parent `Workspace` object in `root:consumers`.

KCP then deletes:

- the `APIBinding` inside the workspace
- the `MongoDBDatabase` objects inside the workspace
- the child `LogicalCluster`

The parent `Workspace` object can remain visible briefly after the child
`LogicalCluster` is already gone. The provisioner UI therefore derives a more
useful status from the child `LogicalCluster`:

- `Deleting content`
- `Finalizing parent`

That reflects real deletion progress more accurately than the raw workspace
phase alone.

Production caveat:
- if a real controller provisions cloud infrastructure, object deletion needs a
  finalizer-driven cleanup path before workspace teardown completes
- otherwise a workspace delete could orphan external resources

## Deploy Flow

The main entrypoint is:

```bash
make deploy
```

The deploy pipeline is:

```text
kind -> helm-repos -> cert-manager -> kcp -> crds -> kro -> kubeconfig
-> bootstrap -> sync-agent -> provisioner -> controllers -> headlamp
```

Important deploy details:

- KCP depends on cert-manager for serving and client certificates.
- `patch-kcp-client-ca` is required so KCP trusts the front-proxy client CA for
  workspace initialization callbacks.
- the KCP admin kubeconfig is materialized into `/tmp/kcp-admin.kubeconfig`
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
- The current graph status section mainly reflects the on-prem branch.
- Headlamp workspace access is centralized through one shared deployment and a
  dynamically maintained kubeconfig secret, not through per-user identities.

## Important Paths

Key paths in the current repository:

- [Makefile](Makefile)
- `config/kro/mongodatabase-rgd.yaml`
- [main.go](cmd/provisioner/main.go)
- [index.html](cmd/provisioner/static/index.html)
- [workspace.go](internal/provisioner/workspace.go)
- [index.tsx](headlamp-plugin/kcp/src/index.tsx)
- `headlamp-plugin/kcp/src/APIBindingsPage.tsx`
- `headlamp-plugin/kcp/src/APIBindingInstancesPage.tsx`
