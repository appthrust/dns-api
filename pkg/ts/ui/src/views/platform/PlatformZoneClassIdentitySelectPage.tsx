/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import type { Provider } from '../../types/resources';
import { platformNamespaces } from './resources';
import {
  PLATFORM_ZONE_CLASSES_PATH,
  PlatformRouteFrame,
  usePlatformNavigation,
  ZONE_CLASSES_BREADCRUMB,
  zoneClassNewPath,
} from './routes';
import { ZoneClassFormPage, ZoneClassIdentitySelectPage } from './ZoneClassForm';

export function PlatformZoneClassIdentitySelectPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const [provider, setProvider] = React.useState<Provider>();
  if (provider) {
    return (
      <PlatformRouteFrame active="zoneclasses">
        <ZoneClassFormPage
          breadcrumb={[ZONE_CLASSES_BREADCRUMB]}
          provider={provider}
          providers={data.providers.items}
          namespaces={platformNamespaces(data)}
          onBack={() => setProvider(undefined)}
          onSaved={() => navigation.push(PLATFORM_ZONE_CLASSES_PATH)}
        />
      </PlatformRouteFrame>
    );
  }
  return (
    <PlatformRouteFrame active="zoneclasses">
      <ZoneClassIdentitySelectPage
        breadcrumb={[ZONE_CLASSES_BREADCRUMB]}
        identities={data.identities.items}
        providers={data.providers.items}
        onSelectProvider={setProvider}
        onCancel={() => navigation.push(PLATFORM_ZONE_CLASSES_PATH)}
        onSelectIdentity={identity => navigation.push(zoneClassNewPath(identity))}
      />
    </PlatformRouteFrame>
  );
}
