/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { EmptyState, Page, ToolbarButton } from '../common/ui';
import {
  findZoneClass,
  useZoneClassRouteParams,
  useZonesNavigation,
  ZONE_CLASS_SELECT_PATH,
  ZONES_BREADCRUMB,
  ZONES_PATH,
} from './routes';
import { ZoneFormPage } from './ZoneForm';

export function ZoneNewPage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  const params = useZoneClassRouteParams();
  const zoneClass = findZoneClass(data.zoneClasses.items, params.namespace, params.name);

  if (!zoneClass) {
    const action = (
      <ToolbarButton
        label="Select ZoneClass"
        icon="mdi:arrow-left"
        tone="secondary"
        onClick={() => navigation.push(ZONE_CLASS_SELECT_PATH)}
      />
    );
    return (
      <Page
        breadcrumb={[ZONES_BREADCRUMB]}
        title="ZoneClass not found"
        description="Choose a visible ZoneClass before creating a Zone."
        actions={action}
      >
        <EmptyState
          title="ZoneClass not found"
          body="The requested ZoneClass is not visible in this cluster."
          action={action}
        />
      </Page>
    );
  }

  return (
    <ZoneFormPage
      breadcrumb={[ZONES_BREADCRUMB]}
      zoneClass={zoneClass}
      onBack={() => navigation.push(ZONE_CLASS_SELECT_PATH)}
      onSaved={() => navigation.push(ZONES_PATH)}
    />
  );
}
