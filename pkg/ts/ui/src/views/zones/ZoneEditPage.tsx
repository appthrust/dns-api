/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { namespaceOf, zoneClassRefNamespace } from '../../resources';
import { EmptyState, Page } from '../common/ui';
import {
  findZone,
  findZoneClass,
  unresolvedBreadcrumb,
  useZoneRouteParams,
  useZonesNavigation,
  zoneBreadcrumb,
  zonePath,
  ZONES_BREADCRUMB,
} from './routes';
import { ZoneFormPage } from './ZoneForm';

export function ZoneEditPage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  const params = useZoneRouteParams();
  const zone = findZone(data.zones.items, params.namespace, params.name);
  const zoneClass = zone
    ? findZoneClass(
        data.zoneClasses.items,
        zoneClassRefNamespace(zone),
        zone.spec.zoneClassRef.name
      )
    : undefined;
  const zoneCrumb = zone
    ? zoneBreadcrumb(zone)
    : unresolvedBreadcrumb(params.name, data.zones.loading);

  if (!zone || !zoneClass) {
    return (
      <Page
        breadcrumb={[ZONES_BREADCRUMB, zoneCrumb]}
        title={!zone ? 'Zone not found' : 'ZoneClass not found'}
        description="The requested resource is not visible in this cluster."
      >
        <EmptyState
          title={!zone ? 'Zone not found' : 'ZoneClass not found'}
          body={
            zone
              ? `${namespaceOf(zone)}/${
                  zone.spec.zoneClassRef.name
                } is not visible in this cluster.`
              : 'The requested Zone is not visible in this cluster.'
          }
        />
      </Page>
    );
  }

  return (
    <ZoneFormPage
      breadcrumb={[ZONES_BREADCRUMB, zoneBreadcrumb(zone)]}
      zoneClass={zoneClass}
      zone={zone}
      onBack={() => navigation.push(zonePath(zone))}
      onSaved={() => navigation.push(zonePath(zone))}
    />
  );
}
