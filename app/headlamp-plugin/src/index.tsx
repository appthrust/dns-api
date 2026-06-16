/** @jsxRuntime classic */
import { SettingsPanel } from '@appthrust/dns-api-ui';
import {
  Headlamp,
  registerPluginSettings,
  registerRoute,
  registerSidebarEntry,
} from '@kinvolk/headlamp-plugin/lib';
import React from 'react';
import { HeadlampPluginRoot } from './HeadlampPluginRoot';
import { pluginName, routes, sidebarEntries } from './routes';

function withDnsRoot(Component: React.ComponentType) {
  return function DnsRoute() {
    return (
      <HeadlampPluginRoot>
        <Component />
      </HeadlampPluginRoot>
    );
  };
}

for (const sidebarEntry of sidebarEntries) {
  registerSidebarEntry(sidebarEntry);
}

for (const route of routes) {
  registerRoute({
    path: route.path,
    sidebar: route.sidebar,
    name: route.name,
    exact: true,
    component: withDnsRoot(route.component),
  });
}

registerPluginSettings(
  pluginName,
  props => (
    <HeadlampPluginRoot>
      <SettingsPanel data={props.data} onDataChange={props.onDataChange ?? (() => undefined)} />
    </HeadlampPluginRoot>
  ),
  true
);

if (Headlamp.isRunningAsApp()) {
  Headlamp.setAppMenu(current => {
    const menus = current ?? [];
    if (menus.some(menu => menu.id === pluginName)) {
      return menus;
    }
    return [
      ...menus,
      {
        id: pluginName,
        label: 'DNS',
        submenu: [
          {
            label: 'Open DNS Overview',
            url: '/dns',
          },
        ],
      },
    ];
  });
}
