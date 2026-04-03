import React from 'react';
import { CommonComponents, Utils } from '@kinvolk/headlamp-plugin/lib';
import {
  EmptyContent,
  Loader,
  NameValueTable,
  ObjectEventList,
  ResourceListView,
  SectionBox,
} from '@kinvolk/headlamp-plugin/lib/CommonComponents';
import { KubeObject } from '@kinvolk/headlamp-plugin/lib/lib/k8s/KubeObject';
import Box from '@mui/material/Box';
import Drawer from '@mui/material/Drawer';
import IconButton from '@mui/material/IconButton';
import Link from '@mui/material/Link';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';
import { Icon } from '@iconify/react';
import { generatePath } from 'react-router';
import { useHistory, useParams } from 'react-router-dom';
import {
  APIBindingResourceMeta,
  decodeResourceKey,
  makeResourceClass,
  makeResourceLabelMap,
  makeResourceRoutePath,
  useAPIBindingResources,
  useRegisterAPIBindingResourceSidebarEntries,
} from './apiBindingResources';

interface InstanceRouteParams {
  resourceKey?: string;
  namespace?: string;
  name?: string;
}

function selectedNamespaceParam(resource: APIBindingResourceMeta, namespace?: string): string {
  return resource.isNamespaced ? namespace || '-' : '-';
}

function pushInstanceRoute(
  history: ReturnType<typeof useHistory>,
  resource: APIBindingResourceMeta,
  cluster: string | null,
  namespace?: string,
  name?: string
) {
  let path = makeResourceRoutePath(resource);
  if (name && name.length > 0) {
    path = `${path}/${selectedNamespaceParam(resource, namespace)}/${encodeURIComponent(name)}`;
  }

  history.push({
    pathname: cluster
      ? generatePath(Utils.getClusterPrefixedPath(path), { cluster })
      : path,
  });
}

function findResourceForItem(
  resources: APIBindingResourceMeta[],
  item: KubeObject
): APIBindingResourceMeta | null {
  const apiVersion = item.jsonData?.apiVersion ?? '';
  const [group = '', version = ''] = apiVersion.includes('/')
    ? apiVersion.split('/')
    : ['', apiVersion];
  const kind = item.kind ?? item.jsonData?.kind ?? '';

  return (
    resources.find(
      resource =>
        resource.group === group &&
        resource.version === version &&
        resource.kind === kind
    ) ?? null
  );
}

function LinkButton({
  label,
  onClick,
}: {
  label: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <Link
      component="button"
      onClick={onClick}
      sx={{
        cursor: 'pointer',
        background: 'none',
        border: 'none',
        padding: 0,
        fontSize: 'inherit',
        fontFamily: 'inherit',
      }}
    >
      {label}
    </Link>
  );
}

function formatScalarValue(value: unknown): string {
  if (value === null || value === undefined) {
    return '—';
  }
  if (typeof value === 'boolean') {
    return value ? 'True' : 'False';
  }
  if (typeof value === 'number' || typeof value === 'bigint') {
    return String(value);
  }
  if (typeof value === 'string') {
    return value.length > 0 ? value : '—';
  }

  return JSON.stringify(value);
}

function splitObjectEntries(value: unknown): {
  scalarEntries: Array<[string, unknown]>;
  nestedEntries: Array<[string, unknown]>;
} {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return { scalarEntries: [], nestedEntries: [] };
  }

  return Object.entries(value as Record<string, unknown>).reduce(
    (acc, [key, entryValue]) => {
      const isNested =
        Array.isArray(entryValue) ||
        (entryValue !== null && typeof entryValue === 'object');

      if (isNested) {
        acc.nestedEntries.push([key, entryValue]);
      } else {
        acc.scalarEntries.push([key, entryValue]);
      }

      return acc;
    },
    {
      scalarEntries: [] as Array<[string, unknown]>,
      nestedEntries: [] as Array<[string, unknown]>,
    }
  );
}

function StructuredValue({
  label,
  value,
  depth = 0,
}: {
  label: string;
  value: unknown;
  depth?: number;
}) {
  if (Array.isArray(value)) {
    if (value.length === 0) {
      return (
        <SectionBox title={label}>
          <Typography variant="body2" color="text.secondary">
            Empty
          </Typography>
        </SectionBox>
      );
    }

    const allScalar = value.every(item => item === null || typeof item !== 'object');
    if (allScalar) {
      return (
        <SectionBox title={label}>
          <Typography
            component="pre"
            sx={{
              m: 0,
              fontFamily: 'monospace',
              fontSize: '0.875rem',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
            }}
          >
            {value.map(item => `- ${formatScalarValue(item)}`).join('\n')}
          </Typography>
        </SectionBox>
      );
    }

    return (
      <SectionBox title={label}>
        <Stack spacing={2}>
          {value.map((item, index) => (
            <StructuredValue
              key={`${label}-${index}`}
              label={`${label} ${index + 1}`}
              value={item}
              depth={depth + 1}
            />
          ))}
        </Stack>
      </SectionBox>
    );
  }

  if (value !== null && typeof value === 'object') {
    const { scalarEntries, nestedEntries } = splitObjectEntries(value);

    return (
      <SectionBox title={label}>
        <Stack spacing={2}>
          {scalarEntries.length > 0 && (
            <NameValueTable
              rows={scalarEntries.map(([name, entryValue]) => ({
                name,
                value: formatScalarValue(entryValue),
              }))}
            />
          )}
          {nestedEntries.map(([name, entryValue]) => (
            <Box key={name} sx={{ pl: depth > 0 ? 2 : 0 }}>
              <StructuredValue label={name} value={entryValue} depth={depth + 1} />
            </Box>
          ))}
        </Stack>
      </SectionBox>
    );
  }

  return (
    <SectionBox title={label}>
      <Typography variant="body2">{formatScalarValue(value)}</Typography>
    </SectionBox>
  );
}

function AggregateInstancesList({
  resources,
}: {
  resources: APIBindingResourceMeta[];
}) {
  const history = useHistory();
  const cluster = Utils.getCluster();
  const labels = React.useMemo(() => makeResourceLabelMap(resources), [resources]);

  const listResults = resources.map(resource => {
    const ResourceClass = makeResourceClass(resource);
    const [items, error] = ResourceClass.useList();
    return { resource, items, error };
  });

  const allFailed = listResults.length > 0 && listResults.every(result => result.error);
  const loading = listResults.some(result => result.items === null && !result.error);
  const data = listResults.flatMap(result => result.items ?? []);

  if (loading) {
    return <Loader title="Loading APIBinding instances…" />;
  }

  if (allFailed) {
    return <EmptyContent color="error">Failed to load APIBinding instances.</EmptyContent>;
  }

  if (data.length === 0) {
    return <EmptyContent>No APIBinding instances found.</EmptyContent>;
  }

  return (
    <ResourceListView
      title="Instances"
      data={data}
      columns={[
        {
          id: 'name',
          label: 'Name',
          getValue: (item: KubeObject) => item.metadata?.name ?? '',
          render: (item: KubeObject) => {
            const resource = findResourceForItem(resources, item);
            if (!resource) {
              return item.metadata?.name ?? '—';
            }

            return (
              <LinkButton
                label={item.metadata?.name}
                onClick={() =>
                  pushInstanceRoute(
                    history,
                    resource,
                    cluster,
                    item.metadata?.namespace,
                    item.metadata?.name
                  )
                }
              />
            );
          },
        },
        {
          id: 'resourceType',
          label: 'Resource',
          getValue: (item: KubeObject) => {
            const resource = findResourceForItem(resources, item);
            return resource ? labels[resource.key] : item.kind ?? '';
          },
          render: (item: KubeObject) => {
            const resource = findResourceForItem(resources, item);
            if (!resource) {
              return item.kind ?? '—';
            }

            return (
              <LinkButton
                label={labels[resource.key]}
                onClick={() => pushInstanceRoute(history, resource, cluster)}
              />
            );
          },
        },
        {
          id: 'api',
          label: 'API',
          getValue: (item: KubeObject) => item.jsonData?.apiVersion ?? '',
        },
        'namespace',
        'age',
      ]}
    />
  );
}

function InstanceDetailPane({
  item,
  resource,
  onClose,
}: {
  item: KubeObject;
  resource: APIBindingResourceMeta;
  onClose: () => void;
}) {
  const handleEditLaunch = React.useCallback(() => {
    window.setTimeout(onClose, 0);
  }, [onClose]);

  return (
    <Box sx={{ width: '50vw', overflowY: 'auto', height: '100%', pt: '64px' }}>
      <Box sx={{ px: 2, pb: 2 }}>
        <CommonComponents.Resource.MainInfoSection
          resource={item}
          backLink={null}
          noDefaultActions
          actions={[
            <Box key="edit" onClickCapture={handleEditLaunch}>
              <CommonComponents.Resource.EditButton item={item} />
            </Box>,
            <IconButton key="close" onClick={onClose} size="small">
              <Icon icon="mdi:close" />
            </IconButton>,
          ]}
          extraInfo={[
            { name: 'Kind', value: resource.kind },
            { name: 'API', value: `${resource.group}/${resource.version}` },
            { name: 'Resource', value: resource.resource },
            {
              name: 'Bindings',
              value: resource.bindingNames.join(', ') || '—',
            },
          ]}
        />

        {item.jsonData?.status?.conditions && (
          <SectionBox title="Conditions">
            <CommonComponents.Resource.ConditionsTable
              resource={item.jsonData}
              showLastUpdate={false}
            />
          </SectionBox>
        )}

        {item.jsonData?.spec && <StructuredValue label="Spec" value={item.jsonData.spec} />}
        {item.jsonData?.status && (
          <StructuredValue
            label="Status"
            value={
              Object.fromEntries(
                Object.entries(item.jsonData.status).filter(([key]) => key !== 'conditions')
              )
            }
          />
        )}

        <ObjectEventList object={item} />
      </Box>
    </Box>
  );
}

function SelectedInstanceDrawer({
  resource,
  name,
  namespace,
  onClose,
}: {
  resource: APIBindingResourceMeta;
  name: string;
  namespace?: string;
  onClose: () => void;
}) {
  const ResourceClass = React.useMemo(() => makeResourceClass(resource), [resource]);
  const selectedNamespace = resource.isNamespaced && namespace !== '-' ? namespace : undefined;
  const [item, error] = ResourceClass.useGet(name, selectedNamespace);

  return (
    <Drawer anchor="right" open onClose={onClose}>
      {!item && !error && <Loader title="Loading resource details…" />}
      {!item && error && (
        <Box sx={{ width: '50vw', p: 2, pt: '80px' }}>
          <Typography color="error" variant="body2">
            {String(error)}
          </Typography>
        </Box>
      )}
      {item && <InstanceDetailPane item={item} resource={resource} onClose={onClose} />}
    </Drawer>
  );
}

export default function APIBindingInstancesPage() {
  const history = useHistory();
  const cluster = Utils.getCluster();
  const { resourceKey, namespace, name } = useParams<InstanceRouteParams>();
  const { resources, error, loading } = useAPIBindingResources();
  useRegisterAPIBindingResourceSidebarEntries(resources);

  const selectedResource = React.useMemo(() => {
    if (!resourceKey) {
      return null;
    }

    const decoded = decodeResourceKey(resourceKey);
    if (!decoded) {
      return null;
    }

    return (
      resources.find(
        resource =>
          resource.group === decoded.group &&
          resource.version === decoded.version &&
          resource.resource === decoded.resource
      ) ?? null
    );
  }, [resourceKey, resources]);

  const labels = React.useMemo(() => makeResourceLabelMap(resources), [resources]);

  const handleClose = React.useCallback(() => {
    if (!selectedResource) {
      return;
    }

    pushInstanceRoute(history, selectedResource, cluster);
  }, [cluster, history, selectedResource]);

  if (loading) {
    return <Loader title="Loading APIBinding resources…" />;
  }

  if (error) {
    return <EmptyContent color="error">{error}</EmptyContent>;
  }

  if (!resourceKey) {
    if (resources.length === 0) {
      return <EmptyContent>No bound APIBinding resources found.</EmptyContent>;
    }

    return <AggregateInstancesList resources={resources} />;
  }

  if (!selectedResource) {
    return <EmptyContent>Unknown APIBinding resource.</EmptyContent>;
  }

  const ResourceClass = makeResourceClass(selectedResource);
  const title = `${labels[selectedResource.key]} Instances`;

  return (
    <>
      <ResourceListView
        title={title}
        resourceClass={ResourceClass}
        headerProps={{
          noNamespaceFilter: !selectedResource.isNamespaced,
          subtitle: (
            <NameValueTable
              rows={[
                { name: 'API', value: `${selectedResource.group}/${selectedResource.version}` },
                { name: 'Bindings', value: selectedResource.bindingNames.join(', ') || '—' },
                { name: 'Cluster', value: cluster ?? '—' },
              ]}
            />
          ),
        }}
        columns={[
          {
            id: 'name',
            label: 'Name',
            getValue: (item: KubeObject) => item.metadata?.name ?? '',
            render: (item: KubeObject) => (
              <LinkButton
                label={item.metadata?.name}
                onClick={() =>
                  pushInstanceRoute(
                    history,
                    selectedResource,
                    cluster,
                    item.metadata?.namespace,
                    item.metadata?.name
                  )
                }
              />
            ),
          },
          ...(selectedResource.isNamespaced ? (['namespace'] as const) : []),
          'age',
        ]}
      />
      {name && (
        <SelectedInstanceDrawer
          resource={selectedResource}
          namespace={namespace}
          name={name}
          onClose={handleClose}
        />
      )}
    </>
  );
}
