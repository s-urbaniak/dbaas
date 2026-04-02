import React from 'react';
import { K8s } from '@kinvolk/headlamp-plugin/lib';

interface LogicalClusterStatus {
  URL?: string;
  phase?: string;
}

interface WorkspaceStatus {
  URL?: string;
  phase?: string;
}

class LogicalCluster extends K8s.KubeObject<Record<string, unknown>, LogicalClusterStatus> {
  static apiVersion = 'core.kcp.io/v1alpha1';
  static kind = 'LogicalCluster';
  static plural = 'logicalclusters';
  static isNamespaced = false;
}

class Workspace extends K8s.KubeObject<Record<string, unknown>, WorkspaceStatus> {
  static apiVersion = 'tenancy.kcp.io/v1alpha1';
  static kind = 'Workspace';
  static plural = 'workspaces';
  static isNamespaced = false;
}

/** Extract workspace path like "root:consumers" from a KCP URL. */
function wsPath(url?: string): string {
  if (!url) return '?';
  const idx = url.indexOf('/clusters/');
  return idx >= 0 ? url.slice(idx + '/clusters/'.length) : url;
}

/** Derive the context name used by the provisioner from the workspace path.
 *  e.g. "root:consumers:test" → "test" (the last segment). */
function ctxName(path: string): string {
  const parts = path.split(':');
  return parts[parts.length - 1];
}

const phaseBadge = (phase?: string): React.CSSProperties => {
  const color =
    phase === 'Ready' ? '#2e7d32' :
    phase === 'Initializing' ? '#ed6c02' :
    phase === 'Terminating' ? '#616161' : '#1565c0';
  return {
    display: 'inline-block',
    padding: '1px 7px',
    borderRadius: 4,
    fontSize: 11,
    fontWeight: 600,
    background: color,
    color: '#fff',
    marginLeft: 8,
  };
};

interface WorkspaceNodeProps {
  name: string;
  phase?: string;
  url?: string;
  currentPath: string;
  indent: number;
}

function WorkspaceNode({ name, phase, url, currentPath, indent }: WorkspaceNodeProps) {
  const childPath = `${currentPath}:${name}`;
  const ctx = ctxName(childPath);
  const headlampURL = `/c/${ctx}`;

  return (
    <div style={{ marginLeft: indent, marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
      <span style={{ color: 'gray', fontSize: 13 }}>{'└─'}</span>
      <a href={headlampURL} style={{ fontWeight: 500, textDecoration: 'none' }}>
        {name}
      </a>
      <span style={phaseBadge(phase)}>{phase ?? '—'}</span>
      {url && (
        <span style={{ fontSize: 11, color: 'gray' }}>
          {wsPath(url)}
        </span>
      )}
    </div>
  );
}

export default function WorkspacesPage() {
  const [lc, lcError] = LogicalCluster.useGet('cluster');
  const [workspaces, wsError] = Workspace.useList();

  const loading = !lc && !lcError;
  if (loading) return <p style={{ padding: 16 }}>Loading…</p>;

  const currentPath = wsPath(lc?.jsonData?.status?.URL);

  return (
    <div style={{ padding: 16 }}>
      <h2 style={{ marginTop: 0 }}>Workspace Hierarchy</h2>

      {lcError && (
        <p style={{ color: 'orange', fontSize: 13 }}>
          Could not load LogicalCluster: {String(lcError)}
        </p>
      )}

      {/* Current workspace root node */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
        <span style={{ fontSize: 18 }}>🗂</span>
        <strong style={{ fontSize: 15 }}>{currentPath}</strong>
        {lc?.jsonData?.status?.phase && (
          <span style={phaseBadge(lc.jsonData.status.phase)}>{lc.jsonData.status.phase}</span>
        )}
        <span style={{ fontSize: 11, color: 'gray' }}>(current workspace)</span>
      </div>

      {/* Child workspaces */}
      {wsError && (
        <p style={{ color: 'orange', fontSize: 13, marginLeft: 24 }}>
          Could not list child workspaces: {String(wsError)}
        </p>
      )}
      {workspaces && workspaces.length === 0 && (
        <p style={{ color: 'gray', fontSize: 13, marginLeft: 24 }}>No child workspaces.</p>
      )}
      {workspaces && workspaces.map(ws => (
        <WorkspaceNode
          key={ws.metadata.name}
          name={ws.metadata.name}
          phase={ws.jsonData.status?.phase}
          url={ws.jsonData.status?.URL}
          currentPath={currentPath}
          indent={24}
        />
      ))}
    </div>
  );
}
