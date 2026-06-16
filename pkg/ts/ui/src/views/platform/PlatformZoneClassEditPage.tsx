/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { namespaceOf, zoneClassIdentityName } from '../../resources';
import { findIdentity, findZoneClass } from './resources';
import {
  PLATFORM_ZONE_CLASSES_PATH,
  PlatformMissingResourcePage,
  PlatformRouteFrame,
  unresolvedBreadcrumb,
  usePlatformNavigation,
  useResourceRouteParams,
  ZONE_CLASSES_BREADCRUMB,
  zoneClassBreadcrumb,
  zoneClassDetailPath,
} from './routes';
import { ZoneClassFormPage } from './ZoneClassForm';

export function PlatformZoneClassEditPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const params = useResourceRouteParams();
  const zoneClass = findZoneClass(data.zoneClasses.items, params.namespace, params.name);
  const zoneClassCrumb = zoneClass
    ? zoneClassBreadcrumb(zoneClass)
    : unresolvedBreadcrumb(params.name, data.zoneClasses.loading);

  if (!zoneClass) {
    return (
      <PlatformMissingResourcePage
        active="zoneclasses"
        breadcrumb={[ZONE_CLASSES_BREADCRUMB, zoneClassCrumb]}
        title="ZoneClass not found"
        body="The requested ZoneClass is not visible in this cluster."
      />
    );
  }

  const identityName = zoneClassIdentityName(zoneClass);
  const identity = identityName
    ? findIdentity(data.identities.items, namespaceOf(zoneClass), identityName)
    : undefined;

  return (
    <PlatformRouteFrame active="zoneclasses">
      <ZoneClassFormPage
        breadcrumb={[ZONE_CLASSES_BREADCRUMB, zoneClassBreadcrumb(zoneClass)]}
        identity={identity}
        zoneClass={zoneClass}
        providers={data.providers.items}
        onBack={() => navigation.push(zoneClassDetailPath(zoneClass))}
        onSaved={() => navigation.push(PLATFORM_ZONE_CLASSES_PATH)}
      />
    </PlatformRouteFrame>
  );
}
