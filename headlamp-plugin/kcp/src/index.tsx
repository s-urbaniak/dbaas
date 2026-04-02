import { registerRoute, registerSidebarEntry } from '@kinvolk/headlamp-plugin/lib';
import APIBindingsPage from './APIBindingsPage';
import WorkspacesPage from './WorkspacesPage';

registerRoute({
  path: '/kcp/apis',
  component: APIBindingsPage,
});

registerRoute({
  path: '/kcp/workspaces',
  component: WorkspacesPage,
});

registerSidebarEntry({
  parent: null,
  name: 'kcp-apis',
  title: 'KCP APIs',
  url: '/kcp/apis',
  icon: 'mdi:api',
});

registerSidebarEntry({
  parent: null,
  name: 'kcp-workspaces',
  title: 'Workspaces',
  url: '/kcp/workspaces',
  icon: 'mdi:file-tree',
});
