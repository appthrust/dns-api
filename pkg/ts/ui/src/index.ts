export { DnsApiApp } from './app/DnsApiApp';
export type { DnsApiPlatform, DnsData, ResourceHandle, ResourceListState } from './platform';
export type { DnsTheme } from './theme';

export { OverviewPage } from './views/OverviewPage';
export { SettingsPanel } from './views/SettingsPanel';
export { EmptyState, Page, ToolbarButton } from './views/common/ui';

export { ZonesPage } from './views/zones/ZonesPage';
export { ZoneClassSelectPage } from './views/zones/ZoneClassSelectPage';
export { ZoneNewPage } from './views/zones/ZoneNewPage';
export { ZoneEditPage } from './views/zones/ZoneEditPage';
export { ZoneDetailPage } from './views/zones/ZoneDetailPage';
export { ZoneDeletePage } from './views/zones/ZoneDeletePage';
export { RecordSetNewPage } from './views/zones/RecordSetNewPage';
export { RecordSetEditPage } from './views/zones/RecordSetEditPage';
export { RecordSetDetailRoutePage } from './views/zones/RecordSetDetailRoutePage';
export { RecordSetDeleteRoutePage } from './views/zones/RecordSetDeleteRoutePage';

export { PlatformPage } from './views/platform/PlatformPage';
export { PlatformIntegrationsPage } from './views/platform/PlatformIntegrationsPage';
export { PlatformIntegrationTypeSelectPage } from './views/platform/PlatformIntegrationTypeSelectPage';
export { PlatformRoute53IntegrationNewPage } from './views/platform/PlatformRoute53IntegrationNewPage';
export { PlatformCloudflareIntegrationNewPage } from './views/platform/PlatformCloudflareIntegrationNewPage';
export { PlatformIntegrationEditPage } from './views/platform/PlatformIntegrationEditPage';
export { PlatformIntegrationDeletePage } from './views/platform/PlatformIntegrationDeletePage';
export { PlatformIntegrationDetailPage } from './views/platform/PlatformIntegrationDetailPage';
export { PlatformZoneClassesPage } from './views/platform/PlatformZoneClassesPage';
export { PlatformZoneClassIdentitySelectPage } from './views/platform/PlatformZoneClassIdentitySelectPage';
export { PlatformZoneClassNewPage } from './views/platform/PlatformZoneClassNewPage';
export { PlatformZoneClassEditPage } from './views/platform/PlatformZoneClassEditPage';
export { PlatformZoneClassDeletePage } from './views/platform/PlatformZoneClassDeletePage';
export { PlatformZoneClassDetailPage } from './views/platform/PlatformZoneClassDetailPage';

export * from './resources';
