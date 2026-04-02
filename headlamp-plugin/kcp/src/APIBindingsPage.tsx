import React from 'react';
import { K8s } from '@kinvolk/headlamp-plugin/lib';

interface BoundResource {
  group: string;
  resource: string;
  storageVersions: string[];
}

interface APIBindingSpec {
  reference?: {
    export?: {
      name?: string;
      path?: string;
    };
  };
}

interface APIBindingStatus {
  phase?: string;
  boundResources?: BoundResource[];
}

class APIBinding extends K8s.KubeObject<APIBindingSpec, APIBindingStatus> {
  static apiVersion = 'apis.kcp.io/v1alpha1';
  static kind = 'APIBinding';
  static plural = 'apibindings';
  static isNamespaced = false;
}

const badge: React.CSSProperties = {
  display: 'inline-block',
  padding: '2px 8px',
  borderRadius: 4,
  fontSize: 12,
  fontWeight: 600,
};

const phaseBadge = (phase?: string): React.CSSProperties => ({
  ...badge,
  background: phase === 'Bound' ? '#2e7d32' : '#ed6c02',
  color: '#fff',
});

const th: React.CSSProperties = {
  textAlign: 'left',
  padding: '4px 16px 4px 0',
  fontWeight: 600,
  borderBottom: '1px solid var(--border-color, #e0e0e0)',
  whiteSpace: 'nowrap',
};

const td: React.CSSProperties = {
  padding: '4px 16px 4px 0',
  verticalAlign: 'top',
};

export default function APIBindingsPage() {
  const [bindings, error] = APIBinding.useList();

  if (error) {
    return <p style={{ color: 'red', padding: 16 }}>Error loading APIBindings: {String(error)}</p>;
  }
  if (!bindings) {
    return <p style={{ padding: 16 }}>Loading…</p>;
  }
  if (bindings.length === 0) {
    return <p style={{ padding: 16, color: 'gray' }}>No APIBindings found in this workspace.</p>;
  }

  return (
    <div style={{ padding: 16 }}>
      <h2 style={{ marginTop: 0 }}>KCP API Bindings</h2>
      {bindings.map(b => {
        const ref = b.jsonData.spec?.reference?.export;
        const bound: BoundResource[] = b.jsonData.status?.boundResources ?? [];
        const phase = b.jsonData.status?.phase;
        return (
          <div key={b.metadata.name} style={{ marginBottom: 32 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 8 }}>
              <strong style={{ fontSize: 16 }}>{b.metadata.name}</strong>
              <span style={phaseBadge(phase)}>{phase ?? 'Unknown'}</span>
            </div>
            {ref && (
              <div style={{ marginBottom: 8, fontSize: 13, color: 'gray' }}>
                Export: <code>{ref.path}/{ref.name}</code>
              </div>
            )}
            {bound.length > 0 ? (
              <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: 13 }}>
                <thead>
                  <tr>
                    <th style={th}>Resource</th>
                    <th style={th}>Group</th>
                    <th style={th}>Storage versions</th>
                  </tr>
                </thead>
                <tbody>
                  {bound.map(r => (
                    <tr key={`${r.group}/${r.resource}`}>
                      <td style={td}>{r.resource}</td>
                      <td style={td}>{r.group || <em style={{ color: 'gray' }}>core</em>}</td>
                      <td style={td}>{(r.storageVersions ?? []).join(', ')}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p style={{ color: 'gray', fontSize: 13 }}>No resources bound yet.</p>
            )}
          </div>
        );
      })}
    </div>
  );
}
