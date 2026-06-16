/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { EmptyState, Page } from '../common/ui';
import { RecordSetFormPage } from './RecordSetForm';
import {
  findZone,
  unresolvedBreadcrumb,
  useZoneRouteParams,
  useZonesNavigation,
  zoneBreadcrumb,
  zonePath,
  ZONES_BREADCRUMB,
} from './routes';

export function RecordSetNewPage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  const params = useZoneRouteParams();
  const zone = findZone(data.zones.items, params.namespace, params.name);

  if (!zone) {
    return (
      <Page
        breadcrumb={[ZONES_BREADCRUMB, unresolvedBreadcrumb(params.name, data.zones.loading)]}
        title="Zone not found"
        description="Choose a visible Zone before creating a RecordSet."
      >
        <EmptyState
          title="Zone not found"
          body="The requested Zone is not visible in this cluster."
        />
      </Page>
    );
  }

  return (
    <RecordSetFormPage
      breadcrumb={[ZONES_BREADCRUMB, zoneBreadcrumb(zone)]}
      zone={zone}
      data={data}
      onBack={() => navigation.push(zonePath(zone))}
      onSaved={() => navigation.push(zonePath(zone))}
    />
  );
}
