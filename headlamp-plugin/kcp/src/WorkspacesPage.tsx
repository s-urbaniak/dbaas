import React, { useState } from 'react';
import { makeCustomResourceClass } from '@kinvolk/headlamp-plugin/lib/Crd';
import { clusterRequest } from '@kinvolk/headlamp-plugin/lib/ApiProxy';
import { SectionBox, NameValueTable } from '@kinvolk/headlamp-plugin/lib/CommonComponents';
import Box from '@mui/material/Box';
import Chip from '@mui/material/Chip';
import Drawer from '@mui/material/Drawer';
import IconButton from '@mui/material/IconButton';
import Link from '@mui/material/Link';
import Typography from '@mui/material/Typography';
import { Icon } from '@iconify/react';

const LogicalCluster = makeCustomResourceClass({
  apiInfo: [{ group: 'core.kcp.io', version: 'v1alpha1' }],
  kind: 'LogicalCluster',
  pluralName: 'logicalclusters',
  singularName: 'logicalcluster',
  isNamespaced: false,
});

const Workspace = makeCustomResourceClass({
  apiInfo: [{ group: 'tenancy.kcp.io', version: 'v1alpha1' }],
  kind: 'Workspace',
  pluralName: 'workspaces',
  singularName: 'workspace',
  isNamespaced: false,
});

// Normalised workspace item — works for both KubeObject results (jsonData wrapper)
// and raw JSON returned by request().
interface WsItem {
  metadata: { name: string; creationTimestamp?: string; labels?: Record<string, string> };
  jsonData: any;
}

function normalize(raw: any): WsItem {
  // KubeObjects from useList() already have .jsonData; plain objects from request() don't.
  if (raw.jsonData) return raw as WsItem;
  return { metadata: raw.metadata ?? {}, jsonData: raw };
}

function wsPath(url?: string): string {
  if (!url) return '';
  const idx = url.indexOf('/clusters/');
  return idx >= 0 ? url.slice(idx + '/clusters/'.length) : url;
}

function phaseBadgeColor(phase?: string): 'success' | 'warning' | 'default' | 'error' {
  if (phase === 'Ready') return 'success';
  if (phase === 'Initializing') return 'warning';
  if (phase === 'Terminating') return 'error';
  return 'default';
}

// ── Detail pane ───────────────────────────────────────────────────────────────

function WorkspaceDetailPane({ item, onClose }: { item: WsItem; onClose: () => void }) {
  const d = item.jsonData;
  const phase = d?.status?.phase;
  const url = d?.spec?.URL;
  const labels = item.metadata?.labels ?? {};

  return (
    <Box sx={{ width: '50vw', p: 2, overflowY: 'auto', height: '100%', pt: '64px' }}>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 1 }}>
        <Typography variant="h6" sx={{ flex: 1 }}>{item.metadata?.name}</Typography>
        <IconButton onClick={onClose} size="small">
          <Icon icon="mdi:close" />
        </IconButton>
      </Box>

      <SectionBox title="Details">
        <NameValueTable
          rows={[
            { name: 'Name', value: item.metadata?.name ?? '—' },
            { name: 'Created', value: item.metadata?.creationTimestamp ?? '—' },
            ...(Object.keys(labels).length > 0
              ? [{ name: 'Labels', value: Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ') }]
              : []),
          ]}
        />
      </SectionBox>

      <SectionBox title="Status">
        <NameValueTable
          rows={[
            { name: 'Phase', value: phase ? <Chip label={phase} color={phaseBadgeColor(phase)} size="small" /> : '—' },
            { name: 'URL', value: url ?? '—' },
          ]}
        />
      </SectionBox>
    </Box>
  );
}

// ── Recursive tree node ───────────────────────────────────────────────────────

function WorkspaceTreeNode({
  ws,
  depth,
  onSelect,
}: {
  ws: WsItem;
  depth: number;
  onSelect: (ws: WsItem) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const [children, setChildren] = useState<WsItem[] | null>(null);
  const [childError, setChildError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const specURL = ws.jsonData?.spec?.URL;
  const phase = ws.jsonData?.status?.phase;
  const path = wsPath(specURL);
  // Headlamp cluster context name is the last segment of the KCP workspace path,
  // e.g. "root:consumers:test" → "test".
  const clusterName = path.split(':').pop() ?? '';

  const handleToggle = () => {
    if (!expanded && children === null && !loading) {
      setLoading(true);
      clusterRequest('/apis/tenancy.kcp.io/v1alpha1/workspaces', { cluster: clusterName })
        .then((data: any) => {
          setChildren((data?.items ?? []).map(normalize));
          setLoading(false);
        })
        .catch((err: any) => {
          setChildError(String(err));
          setLoading(false);
        });
    }
    setExpanded(e => !e);
  };

  const indent = depth * 24;

  return (
    <>
      <Box sx={{ pl: `${indent}px`, mb: 0.5, display: 'flex', alignItems: 'center', gap: 0.5 }}>
        <IconButton size="small" onClick={handleToggle} sx={{ p: 0.25 }}>
          <Icon
            icon={loading ? 'mdi:loading' : expanded ? 'mdi:chevron-down' : 'mdi:chevron-right'}
            width={18}
            style={loading ? { animation: 'spin 1s linear infinite' } : undefined}
          />
        </IconButton>
        <Link
          component="button"
          onClick={() => onSelect(ws)}
          sx={{ fontWeight: 500, cursor: 'pointer', background: 'none', border: 'none', padding: 0, fontSize: 'inherit', fontFamily: 'inherit' }}
        >
          {ws.metadata?.name}
        </Link>
        {phase && (
          <Chip label={phase} color={phaseBadgeColor(phase)} size="small" sx={{ fontWeight: 600 }} />
        )}
        {path && (
          <Typography variant="caption" color="text.secondary">{path}</Typography>
        )}
      </Box>

      {expanded && (
        <Box>
          {childError && (
            <Typography color="error" variant="caption" sx={{ pl: `${indent + 42}px` }}>
              {childError}
            </Typography>
          )}
          {!loading && children?.length === 0 && (
            <Typography variant="caption" color="text.secondary" sx={{ pl: `${indent + 42}px` }}>
              No child workspaces
            </Typography>
          )}
          {children?.map(child => (
            <WorkspaceTreeNode
              key={child.metadata?.name}
              ws={child}
              depth={depth + 1}
              onSelect={onSelect}
            />
          ))}
        </Box>
      )}
    </>
  );
}

// ── Page ──────────────────────────────────────────────────────────────────────

export default function WorkspacesPage() {
  const [selectedWs, setSelectedWs] = useState<WsItem | null>(null);
  const [lc, lcError] = LogicalCluster.useGet('cluster') as [any, any];
  const [workspaces, wsError] = Workspace.useList() as [any[] | null, any];

  const loading = !lc && !lcError;
  if (loading) return <Typography sx={{ p: 2 }}>Loading…</Typography>;

  const currentPath = wsPath(lc?.jsonData?.status?.URL);
  const lcPhase = lc?.jsonData?.status?.phase;

  return (
    <>
      <Box sx={{ p: 2 }}>
        <Typography variant="h5" sx={{ mb: 2 }}>Workspace Hierarchy</Typography>

        {lcError && (
          <Typography color="warning.main" variant="body2" sx={{ mb: 1 }}>
            Could not load LogicalCluster: {String(lcError)}
          </Typography>
        )}

        {/* Current workspace root node */}
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1.5 }}>
          <Icon icon="mdi:folder-open" width={20} />
          <Typography variant="subtitle1" fontWeight={600}>{currentPath}</Typography>
          {lcPhase && <Chip label={lcPhase} color={phaseBadgeColor(lcPhase)} size="small" sx={{ fontWeight: 600 }} />}
          <Typography variant="caption" color="text.secondary">(current workspace)</Typography>
        </Box>

        {wsError && (
          <Typography color="warning.main" variant="body2" sx={{ ml: 1 }}>
            Could not list child workspaces: {String(wsError)}
          </Typography>
        )}
        {workspaces && workspaces.length === 0 && (
          <Typography variant="body2" color="text.secondary" sx={{ ml: 1 }}>No child workspaces.</Typography>
        )}
        {workspaces && workspaces.map((ws: any) => (
          <WorkspaceTreeNode
            key={ws.metadata?.name}
            ws={normalize(ws)}
            depth={0}
            onSelect={setSelectedWs}
          />
        ))}
      </Box>

      <Drawer anchor="right" open={!!selectedWs} onClose={() => setSelectedWs(null)}>
        {selectedWs && <WorkspaceDetailPane item={selectedWs} onClose={() => setSelectedWs(null)} />}
      </Drawer>
    </>
  );
}
