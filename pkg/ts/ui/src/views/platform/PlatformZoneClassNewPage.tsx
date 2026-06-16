/** @jsxRuntime classic */
import React from 'react';
import { useParams } from 'react-router-dom';
import { useDnsData } from '../../api/dns';
import { findIdentity } from './resources';
import {
  PLATFORM_ZONE_CLASS_NEW_PATH,
  PLATFORM_ZONE_CLASSES_PATH,
  PlatformMissingResourcePage,
  PlatformRouteFrame,
  routeParam,
  usePlatformNavigation,
  ZONE_CLASSES_BREADCRUMB,
} from './routes';
import { ZoneClassFormPage } from './ZoneClassForm';

type ZoneClassIdentityRouteParams = {
  identityNamespace?: string;
  identityName?: string;
};

export function PlatformZoneClassNewPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const params = useParams<ZoneClassIdentityRouteParams>();
  const identity = findIdentity(
    data.identities.items,
    routeParam(params.identityNamespace),
    routeParam(params.identityName)
  );

  if (!identity) {
    return (
      <PlatformMissingResourcePage
        active="zoneclasses"
        breadcrumb={[ZONE_CLASSES_BREADCRUMB]}
        title="Provider Identity not found"
        body="Choose a visible Provider Identity before creating a ZoneClass."
        backLabel="Select Provider Identity"
        backPath={PLATFORM_ZONE_CLASS_NEW_PATH}
      />
    );
  }

  return (
    <PlatformRouteFrame active="zoneclasses">
      <ZoneClassFormPage
        breadcrumb={[ZONE_CLASSES_BREADCRUMB]}
        identity={identity}
        providers={data.providers.items}
        onBack={() => navigation.push(PLATFORM_ZONE_CLASS_NEW_PATH)}
        onSaved={() => navigation.push(PLATFORM_ZONE_CLASSES_PATH)}
      />
    </PlatformRouteFrame>
  );
}
