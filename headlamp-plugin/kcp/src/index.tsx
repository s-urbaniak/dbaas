import { registerRoute, registerSidebarEntry } from '@kinvolk/headlamp-plugin/lib';
import APIBindingsPage from './APIBindingsPage';
import WorkspacesPage from './WorkspacesPage';

registerSidebarEntry({
  parent: null,
  name: 'kcp',
  label: 'KCP',
  url: '/kcp/apibindings',
  useClusterURL: true,
  icon: 'mdi:server-network',
  sidebar: 'IN-CLUSTER',
});

registerSidebarEntry({
  parent: 'kcp',
  name: 'kcp-apibindings',
  label: 'API Bindings',
  url: '/kcp/apibindings',
  useClusterURL: true,
  sidebar: 'IN-CLUSTER',
});

registerSidebarEntry({
  parent: 'kcp',
  name: 'kcp-workspaces',
  label: 'Workspaces',
  url: '/kcp/workspaces',
  useClusterURL: true,
  sidebar: 'IN-CLUSTER',
});

registerRoute({
  path: '/kcp/apibindings',
  component: APIBindingsPage,
  useClusterURL: true,
  sidebar: 'kcp-apibindings',
  name: 'kcp-apibindings',
});

registerRoute({
  path: '/kcp/workspaces',
  component: WorkspacesPage,
  useClusterURL: true,
  sidebar: 'kcp-workspaces',
  name: 'kcp-workspaces',
});
