import React, { useState } from 'react';
import { makeCustomResourceClass } from '@kinvolk/headlamp-plugin/lib/Crd';
import { SectionBox, NameValueTable, MetadataDisplay } from '@kinvolk/headlamp-plugin/lib/CommonComponents';
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

function WorkspaceDetailPane({ item, onClose }: { item: any; onClose: () => void }) {
  const phase = item.jsonData?.status?.phase;
  const url = item.jsonData?.spec?.URL;

  return (
    <Box sx={{ width: '50vw', p: 2, overflowY: 'auto', height: '100%', pt: '64px' }}>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 1 }}>
        <Typography variant="h6" sx={{ flex: 1 }}>{item.metadata?.name}</Typography>
        <IconButton onClick={onClose} size="small">
          <Icon icon="mdi:close" />
        </IconButton>
      </Box>

      <MetadataDisplay resource={item} />

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

interface WorkspaceNodeProps {
  name: string;
  phase?: string;
  url?: string;
  currentPath: string;
  indent: number;
  onClick: () => void;
}

function WorkspaceNode({ name, phase, url, currentPath, indent, onClick }: WorkspaceNodeProps) {
  return (
    <Box sx={{ ml: `${indent}px`, mb: 0.75, display: 'flex', alignItems: 'center', gap: 1 }}>
      <Typography variant="body2" color="text.secondary">└─</Typography>
      <Link
        component="button"
        onClick={onClick}
        sx={{ fontWeight: 500, cursor: 'pointer', background: 'none', border: 'none', padding: 0, fontSize: 'inherit', fontFamily: 'inherit' }}
      >
        {name}
      </Link>
      {phase && <Chip label={phase} color={phaseBadgeColor(phase)} size="small" sx={{ fontWeight: 600 }} />}
      {url && <Typography variant="caption" color="text.secondary">{wsPath(url)}</Typography>}
    </Box>
  );
}

export default function WorkspacesPage() {
  const [selectedWs, setSelectedWs] = useState<any>(null);
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
          <Typography color="warning.main" variant="body2" sx={{ ml: 3 }}>
            Could not list child workspaces: {String(wsError)}
          </Typography>
        )}
        {workspaces && workspaces.length === 0 && (
          <Typography variant="body2" color="text.secondary" sx={{ ml: 3 }}>No child workspaces.</Typography>
        )}
        {workspaces && workspaces.map((ws: any) => (
          <WorkspaceNode
            key={ws.metadata?.name}
            name={ws.metadata?.name}
            phase={ws.jsonData?.status?.phase}
            url={ws.jsonData?.spec?.URL}
            currentPath={currentPath}
            indent={24}
            onClick={() => setSelectedWs(ws)}
          />
        ))}
      </Box>

      <Drawer anchor="right" open={!!selectedWs} onClose={() => setSelectedWs(null)}>
        {selectedWs && <WorkspaceDetailPane item={selectedWs} onClose={() => setSelectedWs(null)} />}
      </Drawer>
    </>
  );
}
