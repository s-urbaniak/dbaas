import React from 'react';
import {
  Utils,
  registerSidebarEntry,
  registerSidebarEntryFilter,
} from '@kinvolk/headlamp-plugin/lib';
import { makeCustomResourceClass } from '@kinvolk/headlamp-plugin/lib/Crd';
import { request } from '@kinvolk/headlamp-plugin/lib/ApiProxy';

const API_BINDINGS_API = '/apis/apis.kcp.io/v1alpha1/apibindings';
const SIDEBAR_ENTRY_PREFIX = 'kcp-apibindings-resource-';

export interface APIBindingResourceMeta {
  key: string;
  id: string;
  group: string;
  version: string;
  resource: string;
  kind: string;
  singularName: string;
  pluralName: string;
  isNamespaced: boolean;
  bindingNames: string[];
}

interface DiscoveryResourceEntry {
  kind?: string;
  name?: string;
  namespaced?: boolean;
  singularName?: string;
}

interface ResourceAccumulator extends APIBindingResourceMeta {}
interface DiscoveryDocument {
  resources?: DiscoveryResourceEntry[];
}

const sidebarEntriesVisible = new Set<string>();
let sidebarFilterRegistered = false;

function ensureSidebarFilterRegistered() {
  if (sidebarFilterRegistered) {
    return;
  }

  registerSidebarEntryFilter(entry => {
    if (entry.name.startsWith(SIDEBAR_ENTRY_PREFIX) && !sidebarEntriesVisible.has(entry.name)) {
      return null;
    }

    return entry;
  });

  sidebarFilterRegistered = true;
}

export function encodeResourceKey(group: string, version: string, resource: string): string {
  return encodeURIComponent([group, version, resource].join('/'));
}

export function decodeResourceKey(resourceKey: string): {
  group: string;
  version: string;
  resource: string;
} | null {
  try {
    const decoded = decodeURIComponent(resourceKey);
    const [group, version, resource] = decoded.split('/');
    if (!group || !version || !resource) {
      return null;
    }

    return { group, version, resource };
  } catch (_error) {
    return null;
  }
}

export function makeResourceSidebarEntryName(resource: APIBindingResourceMeta): string {
  return `${SIDEBAR_ENTRY_PREFIX}${resource.id}`;
}

export function makeResourceRoutePath(resource: APIBindingResourceMeta): string {
  return `/kcp/apibindings/instances/${resource.key}`;
}

export function makeResourceLabelMap(resources: APIBindingResourceMeta[]): Record<string, string> {
  const counts = resources.reduce<Record<string, number>>((acc, resource) => {
    acc[resource.kind] = (acc[resource.kind] ?? 0) + 1;
    return acc;
  }, {});

  return resources.reduce<Record<string, string>>((acc, resource) => {
    acc[resource.key] =
      counts[resource.kind] > 1 ? `${resource.kind} (${resource.group})` : resource.kind;
    return acc;
  }, {});
}

export function makeResourceClass(resource: APIBindingResourceMeta) {
  return makeCustomResourceClass({
    apiInfo: [{ group: resource.group, version: resource.version }],
    kind: resource.kind,
    pluralName: resource.pluralName,
    singularName: resource.singularName || resource.kind.toLowerCase(),
    isNamespaced: resource.isNamespaced,
  });
}

async function fetchAPIBindings(cluster?: string | null): Promise<any[]> {
  const response = await request(API_BINDINGS_API, { cluster: cluster ?? undefined });
  return response?.items ?? [];
}

function getObjectData(item: any): any {
  return item?.jsonData ?? item ?? {};
}

function getBoundResources(item: any): any[] {
  return getObjectData(item)?.status?.boundResources ?? [];
}

async function discoverResource(
  discoveryPromise: Promise<DiscoveryDocument>,
  resource: string
): Promise<DiscoveryResourceEntry | null> {
  const discovery = await discoveryPromise;
  return (
    (discovery?.resources ?? []).find((item: DiscoveryResourceEntry) => item.name === resource) ?? null
  );
}

function buildBaseAPIBindingResources(bindings: any[]): APIBindingResourceMeta[] {
  const accumulators = new Map<string, ResourceAccumulator>();

  bindings.forEach((binding: any) => {
    const bindingData = getObjectData(binding);
    const bindingName = bindingData.metadata?.name ?? '';

    getBoundResources(binding).forEach((bound: any) => {
      const group = bound.group ?? '';
      const version = bound.storageVersions?.[0] ?? '';
      const resource = bound.resource ?? '';

      if (!group || !version || !resource) {
        return;
      }

      const id = `${group}__${version}__${resource}`.replace(/[^a-zA-Z0-9_-]/g, '-');
      const key = encodeResourceKey(group, version, resource);
      const existing = accumulators.get(key);

      if (existing) {
        if (bindingName && !existing.bindingNames.includes(bindingName)) {
          existing.bindingNames.push(bindingName);
        }
        return;
      }

      accumulators.set(key, {
        key,
        id,
        group,
        version,
        resource,
        kind: resource,
        singularName: resource,
        pluralName: resource,
        isNamespaced: true,
        bindingNames: bindingName ? [bindingName] : [],
      });
    });
  });

  return Array.from(accumulators.values()).sort((a, b) => {
    const byKind = a.kind.localeCompare(b.kind);
    if (byKind !== 0) {
      return byKind;
    }

    return a.group.localeCompare(b.group);
  });
}

function mergeDiscoveredAPIBindingResources(
  resources: APIBindingResourceMeta[],
  discoveries: Map<string, DiscoveryResourceEntry | null>
): APIBindingResourceMeta[] {
  return resources
    .map(resource => {
      const discovered = discoveries.get(resource.key);
      if (!discovered) {
        return resource;
      }

      return {
        ...resource,
        kind: discovered.kind ?? resource.kind,
        isNamespaced:
          typeof discovered.namespaced === 'boolean' ? discovered.namespaced : resource.isNamespaced,
        singularName: discovered.singularName || resource.singularName,
        pluralName: discovered.name || resource.pluralName,
      };
    })
    .sort((a, b) => {
      const byKind = a.kind.localeCompare(b.kind);
      if (byKind !== 0) {
        return byKind;
      }

      return a.group.localeCompare(b.group);
    });
}

async function loadDiscoveredAPIBindingResources(
  resources: APIBindingResourceMeta[],
  cluster?: string | null
): Promise<APIBindingResourceMeta[]> {
  const discoveryRequests = new Map<string, Promise<DiscoveryDocument>>();
  const discoveries = await Promise.all(
    resources.map(async resource => {
      const discoveryKey = `${cluster ?? ''}:${resource.group}/${resource.version}`;
      const discoveryPromise =
        discoveryRequests.get(discoveryKey) ??
        request(`/apis/${resource.group}/${resource.version}`, { cluster: cluster ?? undefined });
      discoveryRequests.set(discoveryKey, discoveryPromise);

      const discovered = await discoverResource(discoveryPromise, resource.resource);
      return [resource.key, discovered] as const;
    })
  );
  return mergeDiscoveredAPIBindingResources(resources, new Map(discoveries));
}

export function useAPIBindingResources() {
  const cluster = Utils.getCluster();
  const [resources, setResources] = React.useState<APIBindingResourceMeta[]>([]);
  const [error, setError] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(true);

  React.useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);

    fetchAPIBindings(cluster)
      .then(bindings => {
        if (cancelled) {
          return;
        }
        const baseResources = buildBaseAPIBindingResources(bindings);
        setResources(baseResources);
        setLoading(false);

        return loadDiscoveredAPIBindingResources(baseResources, cluster)
          .then(nextResources => {
            if (cancelled) {
              return;
            }
            setResources(nextResources);
          })
          .catch(err => {
            if (cancelled) {
              return;
            }
            // Keep the base resources rendered even if discovery refinement fails.
            console.warn('Failed to refine APIBinding resource metadata', err);
          });
      })
      .catch(err => {
        if (cancelled) {
          return;
        }
        setError(String(err));
        setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [cluster]);

  return { resources, error, loading };
}

export function useRegisterAPIBindingResourceSidebarEntries(resources: APIBindingResourceMeta[]) {
  React.useEffect(() => {
    ensureSidebarFilterRegistered();
    sidebarEntriesVisible.clear();

    resources.forEach(resource => {
      const entryName = makeResourceSidebarEntryName(resource);
      sidebarEntriesVisible.add(entryName);
    });
  }, [resources]);

  React.useEffect(() => {
    if (resources.length === 0) {
      return;
    }

    const labels = makeResourceLabelMap(resources);

    resources.forEach(resource => {
      const entryName = makeResourceSidebarEntryName(resource);
      registerSidebarEntry({
        parent: 'kcp-apibindings-instances',
        name: entryName,
        label: labels[resource.key],
        url: makeResourceRoutePath(resource),
        useClusterURL: true,
        sidebar: 'IN-CLUSTER',
      });
    });
  }, [resources]);
}
