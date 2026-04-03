# DBaaS Architecture and Operations

This document describes the repository as it exists today. It replaces the
older design-plan version of `dbaas.md`, which no longer matched the running
system after the Headlamp integration, provisioner reconciliation logic, and
KCP SDK client refactor landed.

## Overview

This repository implements a local multi-tenant DBaaS demo on top of KCP,
kro, and a Kubernetes cluster.

The key idea is:

- KCP provides tenant workspaces.
- kro defines a single `MongoDBDatabase` API.
- The API Sync Agent exports that API from the provider workspace and syncs
  instances between KCP and the physical cluster.
- Mock controllers reconcile the generated backend resources.
- A small provisioner creates consumer workspaces and keeps Headlamp scoped to
  the currently active tenant workspaces.

## Current Topology

```text
Physical Kubernetes cluster
|
|-- cert-manager
|   `-- issues KCP serving and client certificates
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

## Major Components

### KCP

KCP hosts two important root-level workspaces:

- `root:dbaas-provider`
- `root:consumers`

`root:dbaas-provider` is the service-provider workspace. The API Sync Agent
publishes the `MongoDBDatabase` API from there.

`root:consumers` is the parent workspace for tenant workspaces. The
provisioner creates child workspaces under it and adds the `dbaas` APIBinding
inside each one.

### kro

kro owns the `ResourceGraphDefinition` in
[config/kro/mongodatabase-rgd.yaml](/home/sur/src/dbaas/config/kro/mongodatabase-rgd.yaml).

Today the generated API is:

- group `kro.run`
- version `v1alpha1`
- kind `MongoDBDatabase`
- resource `mongodbdatabases`

The graph uses `includeWhen` to branch on `spec.provider`:

- `ON-PREMISE` creates `mongodb.com/v1 MongoDB`
- `AWS` or `AZURE` creates `atlas.generated.mongodb.com/v1 FlexCluster`

The current status block only copies status from the on-prem branch. That is a
pragmatic limitation of the current kro expression behavior and the way this
demo graph is written.

### API Sync Agent

The API Sync Agent exports the generated `MongoDBDatabase` API from
`root:dbaas-provider` and synchronizes instances and status between KCP and
the physical cluster.

This is why deploy order matters:

- MCK and Atlas CRDs must exist before kro applies the graph.
- kro must apply the graph before the sync agent starts.
- the sync agent must see the generated `mongodbdatabases.kro.run` resource at
  startup.

### Mock controllers

The mock controllers live in:

- [mongodb_controller.go](/home/sur/src/dbaas/internal/controller/mongodb_controller.go)
- [flexcluster_controller.go](/home/sur/src/dbaas/internal/controller/flexcluster_controller.go)

They intentionally simulate backend behavior rather than provisioning real
infrastructure.

The MongoDB mock writes status onto `mongodb.com/v1 MongoDB`.

The FlexCluster mock writes status onto
`atlas.generated.mongodb.com/v1 FlexCluster`.

### Provisioner

The provisioner is a small Go HTTP server in
[main.go](/home/sur/src/dbaas/cmd/provisioner/main.go) backed by workspace
logic in
[workspace.go](/home/sur/src/dbaas/internal/provisioner/workspace.go).

It now uses the published KCP SDK clientset directly for KCP-native resources:

- `tenancy.kcp.io/v1alpha1 Workspace`
- `apis.kcp.io/v1alpha1 APIBinding`
- `core.kcp.io/v1alpha1 LogicalCluster`

It still uses the dynamic client only for counting `mongodbdatabases.kro.run`
objects in tenant workspaces.

Current provisioner endpoints:

- `GET /`
- `POST /api/workspaces`
- `GET /api/workspaces/{name}/kubeconfig`
- `POST /api/workspaces/{name}/delete`

Current provisioner responsibilities:

- create a consumer workspace under `root:consumers`
- wait for the workspace to become ready and expose `Spec.URL`
- create the `dbaas` APIBinding in that workspace
- generate a tenant kubeconfig
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

The KCP Headlamp plugin currently provides:

- a Workspaces view
- an API Bindings view
- an API Binding Instances view
- right-side instance details with an edit action

## Resource Lifecycle

The typical tenant flow is:

1. A user creates a workspace in the provisioner UI.
2. The provisioner creates `root:consumers:<name>`.
3. The provisioner creates `APIBinding/dbaas` in that workspace.
4. The user downloads a kubeconfig or opens the workspace in Headlamp.
5. The user creates a `MongoDBDatabase` object in the tenant workspace.
6. The API Sync Agent mirrors that object into the physical cluster.
7. kro creates either a `MongoDB` or `FlexCluster` child resource.
8. The corresponding mock controller writes backend status.
9. kro updates `MongoDBDatabase.status`.
10. The API Sync Agent syncs that status back to the tenant workspace.

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

That makes the UI reflect real deletion progress more accurately than the raw
workspace phase alone.

## Deploy Flow

The main entrypoint is:

```bash
make deploy
```

The current deploy pipeline is:

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

## Important Paths

Key paths in the current repository:

- [Makefile](/home/sur/src/dbaas/Makefile)
- [mongodatabase-rgd.yaml](/home/sur/src/dbaas/config/kro/mongodatabase-rgd.yaml)
- [main.go](/home/sur/src/dbaas/cmd/provisioner/main.go)
- [index.html](/home/sur/src/dbaas/cmd/provisioner/static/index.html)
- [workspace.go](/home/sur/src/dbaas/internal/provisioner/workspace.go)
- [index.tsx](/home/sur/src/dbaas/headlamp-plugin/kcp/src/index.tsx)
- [APIBindingsPage.tsx](/home/sur/src/dbaas/headlamp-plugin/kcp/src/APIBindingsPage.tsx)
- [APIBindingInstancesPage.tsx](/home/sur/src/dbaas/headlamp-plugin/kcp/src/APIBindingInstancesPage.tsx)

## Known Constraints

- Tenant kubeconfigs are still generated from the admin credentials for this
  demo. That is acceptable for local development only.
- The current graph status section mainly reflects the on-prem branch.
- Headlamp workspace access is centralized through one shared deployment and a
  dynamically maintained kubeconfig secret, not through per-user identities.

## Recommended Reading Order

For the shortest accurate path through the codebase:

1. [README.md](/home/sur/src/dbaas/README.md)
2. [Makefile](/home/sur/src/dbaas/Makefile)
3. [mongodatabase-rgd.yaml](/home/sur/src/dbaas/config/kro/mongodatabase-rgd.yaml)
4. [workspace.go](/home/sur/src/dbaas/internal/provisioner/workspace.go)
5. [APIBindingInstancesPage.tsx](/home/sur/src/dbaas/headlamp-plugin/kcp/src/APIBindingInstancesPage.tsx)
