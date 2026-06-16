export const sidebarEntries = [
  {
    parent: null,
    name: 'dns-api',
    label: 'DNS',
    url: '/dns',
    icon: 'mdi:dns',
  },
  {
    parent: 'dns-api',
    name: 'dns-api-zones',
    label: 'Zones',
    url: '/dns/zones',
  },
  {
    parent: 'dns-api',
    name: 'dns-api-platform-zoneclasses',
    label: 'Zone Classes',
    url: '/dns/platform/zoneclasses',
  },
  {
    parent: 'dns-api',
    name: 'dns-api-platform-integrations',
    label: 'Provider Identities',
    url: '/dns/platform/integrations',
  },
];
