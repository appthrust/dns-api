/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import {
  PLATFORM_ZONE_CLASSES_PATH,
  PlatformRouteFrame,
  usePlatformNavigation,
  ZONE_CLASSES_BREADCRUMB,
  zoneClassNewPath,
} from './routes';
import { ZoneClassIdentitySelectPage } from './ZoneClassForm';

export function PlatformZoneClassIdentitySelectPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  return (
    <PlatformRouteFrame active="zoneclasses">
      <ZoneClassIdentitySelectPage
        breadcrumb={[ZONE_CLASSES_BREADCRUMB]}
        identities={data.identities.items}
        onCancel={() => navigation.push(PLATFORM_ZONE_CLASSES_PATH)}
        onSelectIdentity={identity => navigation.push(zoneClassNewPath(identity))}
      />
    </PlatformRouteFrame>
  );
}
