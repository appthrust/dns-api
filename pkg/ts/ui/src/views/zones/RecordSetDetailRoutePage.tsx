/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { EmptyState, Page } from '../common/ui';
import { RecordSetDetailPage } from './RecordSetPages';
import {
  findRecordSet,
  findZone,
  recordSetDeletePath,
  recordSetEditPath,
  unresolvedBreadcrumb,
  useRecordSetRouteParams,
  useZonesNavigation,
  zoneBreadcrumb,
  ZONES_BREADCRUMB,
} from './routes';

export function RecordSetDetailRoutePage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  const params = useRecordSetRouteParams();
  const zone = findZone(data.zones.items, params.namespace, params.name);
  const recordSet = findRecordSet(data.recordSets.items, params.recordNamespace, params.recordName);
  const zoneCrumb = zone
    ? zoneBreadcrumb(zone)
    : unresolvedBreadcrumb(params.name, data.zones.loading);

  if (!zone || !recordSet) {
    return (
      <Page
        breadcrumb={[ZONES_BREADCRUMB, zoneCrumb]}
        title={!zone ? 'Zone not found' : 'RecordSet not found'}
        description="The requested resource is not visible in this cluster."
      >
        <EmptyState
          title={!zone ? 'Zone not found' : 'RecordSet not found'}
          body="The requested resource is not visible in this cluster."
        />
      </Page>
    );
  }

  return (
    <RecordSetDetailPage
      breadcrumb={[ZONES_BREADCRUMB, zoneBreadcrumb(zone)]}
      zone={zone}
      recordSet={recordSet}
      data={data}
      onEdit={() => navigation.push(recordSetEditPath(zone, recordSet))}
      onDelete={() => navigation.push(recordSetDeletePath(zone, recordSet))}
    />
  );
}
