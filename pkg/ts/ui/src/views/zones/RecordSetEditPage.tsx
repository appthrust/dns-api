/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { EmptyState, Page } from '../common/ui';
import { RecordSetFormPage } from './RecordSetForm';
import {
  findRecordSet,
  findZone,
  recordSetBreadcrumb,
  recordSetPath,
  unresolvedBreadcrumb,
  useRecordSetRouteParams,
  useZonesNavigation,
  zoneBreadcrumb,
  ZONES_BREADCRUMB,
} from './routes';

export function RecordSetEditPage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  const params = useRecordSetRouteParams();
  const zone = findZone(data.zones.items, params.namespace, params.name);
  const recordSet = findRecordSet(data.recordSets.items, params.recordNamespace, params.recordName);
  const zoneCrumb = zone
    ? zoneBreadcrumb(zone)
    : unresolvedBreadcrumb(params.name, data.zones.loading);
  const recordCrumb =
    zone && recordSet
      ? recordSetBreadcrumb(zone, recordSet)
      : unresolvedBreadcrumb(params.recordName, data.recordSets.loading);

  if (!zone || !recordSet) {
    return (
      <Page
        breadcrumb={[ZONES_BREADCRUMB, zoneCrumb, recordCrumb]}
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
    <RecordSetFormPage
      breadcrumb={[ZONES_BREADCRUMB, zoneBreadcrumb(zone), recordSetBreadcrumb(zone, recordSet)]}
      zone={zone}
      data={data}
      recordSet={recordSet}
      onBack={() => navigation.push(recordSetPath(zone, recordSet))}
      onSaved={() => navigation.push(recordSetPath(zone, recordSet))}
    />
  );
}
