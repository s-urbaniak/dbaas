import React, { useState, useEffect } from 'react';
import { makeCustomResourceClass } from '@kinvolk/headlamp-plugin/lib/Crd';
import { request } from '@kinvolk/headlamp-plugin/lib/ApiProxy';
import {
  SectionBox,
  SimpleTable,
  NameValueTable,
  Loader,
} from '@kinvolk/headlamp-plugin/lib/CommonComponents';
import Box from '@mui/material/Box';
import Chip from '@mui/material/Chip';
import Drawer from '@mui/material/Drawer';
import IconButton from '@mui/material/IconButton';
import Link from '@mui/material/Link';
import Typography from '@mui/material/Typography';
import { Icon } from '@iconify/react';

const APIBinding = makeCustomResourceClass({
  apiInfo: [{ group: 'apis.kcp.io', version: 'v1alpha1' }],
  kind: 'APIBinding',
  pluralName: 'apibindings',
  singularName: 'apibinding',
  isNamespaced: false,
});

// ── Flat row type ─────────────────────────────────────────────────────────────

interface BoundRow {
  group: string;
  resource: string;
  storageVersions: string[];
  schemaName: string;
  bindingName: string;
}

// ── Schema tree ───────────────────────────────────────────────────────────────

const TYPE_COLORS: Record<string, 'info' | 'warning' | 'secondary' | 'default' | 'success'> = {
  string: 'info',
  integer: 'warning',
  number: 'warning',
  boolean: 'secondary',
  object: 'default',
  array: 'success',
};

function typeLabel(schema: any): string {
  if (schema.type === 'array') {
    const itemType = schema.items?.type ?? 'object';
    return `array[${itemType}]`;
  }
  return schema.type ?? (schema.properties ? 'object' : '—');
}

function SchemaNode({ name, schema, depth }: { name: string; schema: any; depth: number }) {
  const hasChildren =
    schema.properties && Object.keys(schema.properties).length > 0;
  const hasArrayItems =
    schema.type === 'array' && schema.items?.properties;
  const expandable = hasChildren || hasArrayItems;
  const [open, setOpen] = useState(depth === 0);

  const childProperties: Record<string, any> =
    hasChildren
      ? schema.properties
      : hasArrayItems
      ? schema.items.properties
      : {};

  const tl = typeLabel(schema);
  const color = TYPE_COLORS[schema.type ?? ''] ?? 'default';

  return (
    <>
      <Box
        sx={{
          display: 'flex',
          alignItems: 'flex-start',
          py: 0.5,
          pl: `${depth * 20 + 8}px`,
          borderBottom: '1px solid',
          borderColor: 'divider',
          '&:hover': { bgcolor: 'action.hover' },
        }}
      >
        <Box sx={{ width: 20, flexShrink: 0, mt: '2px' }}>
          {expandable && (
            <IconButton size="small" onClick={() => setOpen(o => !o)} sx={{ p: 0 }}>
              <Icon icon={open ? 'mdi:chevron-down' : 'mdi:chevron-right'} width={16} />
            </IconButton>
          )}
        </Box>
        <Typography
          variant="body2"
          sx={{ fontFamily: 'monospace', fontWeight: 500, minWidth: 160, mr: 1, wordBreak: 'break-all' }}
        >
          {name}
        </Typography>
        <Box sx={{ minWidth: 110, mr: 1 }}>
          <Chip label={tl} color={color} size="small" variant="outlined" />
        </Box>
        <Typography variant="caption" color="text.secondary" sx={{ flex: 1 }}>
          {schema.description ?? ''}
        </Typography>
      </Box>

      {expandable && open && Object.entries(childProperties).map(([k, v]: [string, any]) => (
        <SchemaNode key={k} name={k} schema={v} depth={depth + 1} />
      ))}
    </>
  );
}

function SchemaTree({ schema }: { schema: any }) {
  const properties: Record<string, any> = schema?.properties ?? {};
  if (Object.keys(properties).length === 0) return null;
  return (
    <Box sx={{ mx: 2, mb: 2, border: '1px solid', borderColor: 'divider', borderRadius: 1, overflow: 'hidden' }}>
      {schema.description && (
        <Box sx={{ px: 2, py: 1, borderBottom: '1px solid', borderColor: 'divider', bgcolor: 'action.selected' }}>
          <Typography variant="caption" color="text.secondary">{schema.description}</Typography>
        </Box>
      )}
      {Object.entries(properties).map(([k, v]: [string, any]) => (
        <SchemaNode key={k} name={k} schema={v} depth={0} />
      ))}
    </Box>
  );
}

// ── Schema detail pane ────────────────────────────────────────────────────────

function SchemaDetail({ row, onClose }: { row: BoundRow; onClose: () => void }) {
  const [kind, setKind] = useState<string | null>(null);
  const [namespaced, setNamespaced] = useState<boolean | null>(null);
  const [openAPISchema, setOpenAPISchema] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  const [fetchError, setFetchError] = useState<string | null>(null);

  useEffect(() => {
    const version = row.storageVersions[0];
    if (!version || !row.group) return;
    setLoading(true);
    setFetchError(null);
    setKind(null);
    setOpenAPISchema(null);

    Promise.all([
      request(`/apis/${row.group}/${version}`),
      request(`/openapi/v3/apis/${row.group}/${version}`),
    ]).then(([discovery, openapi]: [any, any]) => {
      const resourceEntry = (discovery?.resources ?? []).find(
        (r: any) => r.name === row.resource
      );
      const k: string | null = resourceEntry?.kind ?? null;
      setKind(k);
      setNamespaced(resourceEntry?.namespaced ?? null);

      if (k) {
        const schemas: Record<string, any> = openapi?.components?.schemas ?? {};
        const match = Object.values(schemas).find((s: any) =>
          (s['x-kubernetes-group-version-kind'] ?? []).some(
            (gvk: any) => gvk.group === row.group && gvk.version === version && gvk.kind === k
          )
        ) ?? null;
        setOpenAPISchema(match);
      }
      setLoading(false);
    }).catch((err: any) => {
      setFetchError(String(err));
      setLoading(false);
    });
  }, [row.group, row.resource, row.storageVersions.join(',')]);

  return (
    <Box sx={{ width: '55vw', overflowY: 'auto', height: '100%', pt: '64px' }}>
      <Box sx={{ display: 'flex', alignItems: 'center', p: 2, pb: 0 }}>
        <Typography variant="h6" sx={{ flex: 1 }}>{row.resource}.{row.group}</Typography>
        <IconButton onClick={onClose} size="small">
          <Icon icon="mdi:close" />
        </IconButton>
      </Box>

      {loading && <Loader title="Loading schema…" />}

      {fetchError && (
        <Box sx={{ p: 2 }}>
          <Typography color="error" variant="body2">{fetchError}</Typography>
        </Box>
      )}

      {!loading && !fetchError && (
        <>
          <SectionBox title="Overview">
            <NameValueTable
              rows={[
                { name: 'Kind', value: kind ?? '—' },
                { name: 'Group', value: row.group },
                { name: 'Scope', value: namespaced === null ? '—' : namespaced ? 'Namespaced' : 'Cluster' },
                { name: 'Resource', value: row.resource },
                { name: 'Versions', value: row.storageVersions.join(', ') },
                { name: 'API Binding', value: row.bindingName },
              ]}
            />
          </SectionBox>

          {openAPISchema && Object.keys(openAPISchema?.properties ?? {}).length > 0 && (
            <SectionBox title="Schema">
              <SchemaTree schema={openAPISchema} />
            </SectionBox>
          )}
        </>
      )}
    </Box>
  );
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function APIBindingsPage() {
  const [selected, setSelected] = useState<BoundRow | null>(null);
  const [bindings, error] = APIBinding.useList() as [any[] | null, any];

  const flatRows: BoundRow[] = (bindings ?? []).flatMap((binding: any) =>
    (binding.jsonData?.status?.boundResources ?? []).map((r: any) => ({
      group: r.group ?? '',
      resource: r.resource ?? '',
      storageVersions: r.storageVersions ?? [],
      schemaName: r.schema?.name ?? '',
      bindingName: binding.metadata?.name ?? '',
    }))
  );

  return (
    <>
      <SectionBox title="API Bindings">
        <SimpleTable
          errorMessage={error ? String(error) : null}
          columns={[
            { label: 'Group', getter: (row: BoundRow) => row.group },
            {
              label: 'Resource',
              getter: (row: BoundRow) => (
                <Link
                  component="button"
                  onClick={() => setSelected(row)}
                  sx={{ cursor: 'pointer', background: 'none', border: 'none', padding: 0, fontSize: 'inherit', fontFamily: 'inherit' }}
                >
                  {row.resource}
                </Link>
              ),
            },
            { label: 'Versions', getter: (row: BoundRow) => row.storageVersions.join(', ') },
            { label: 'API Binding', getter: (row: BoundRow) => row.bindingName },
          ]}
          data={flatRows.length > 0 ? flatRows : (bindings === null ? null : [])}
        />
      </SectionBox>

      <Drawer anchor="right" open={!!selected} onClose={() => setSelected(null)}>
        {selected && (
          <SchemaDetail row={selected} onClose={() => setSelected(null)} />
        )}
      </Drawer>
    </>
  );
}
