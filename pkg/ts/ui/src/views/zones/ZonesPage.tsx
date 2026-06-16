/** @jsxRuntime classic */
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import React from 'react';
import { useAccess, useDnsData } from '../../api/dns';
import {
  nameOf,
  namespaceOf,
  resourceKey,
  zoneClassRefNamespace,
  zoneRefNamespace,
  ZoneResource,
} from '../../resources';
import type { RecordSet, Zone } from '../../types/resources';
import {
  ageOf,
  Conditions,
  DataGridTable,
  DesignLinkButton,
  EmptyState,
  GridCell,
  GridHeader,
  GridRow,
  Page,
  ToolbarButton,
  ui,
} from '../common/ui';
import { useZonesNavigation, ZONE_CLASS_SELECT_PATH, zonePath } from './routes';

export function ZonesPage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  const canCreate = useAccess(ZoneResource, 'create', {
    group: 'dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'zones',
  });

  return (
    <Page
      title="Zones"
      description="Public DNS zones managed by DNS API. RecordSet management starts from a Zone detail page."
      actions={
        <ToolbarButton
          label="Create Zone"
          icon="mdi:plus"
          disabled={!canCreate}
          onClick={() => navigation.push(ZONE_CLASS_SELECT_PATH)}
        />
      }
    >
      {data.zones.items.length ? (
        <ZonesTable
          recordSets={data.recordSets.items}
          zones={data.zones.items}
          onOpenZone={zone => navigation.push(zonePath(zone))}
        />
      ) : (
        <EmptyState
          title="No Zones"
          body="Create a Zone to manage a public DNS namespace and its RecordSets."
          action={
            <ToolbarButton
              label="Create Zone"
              icon="mdi:plus"
              disabled={!canCreate}
              onClick={() => navigation.push(ZONE_CLASS_SELECT_PATH)}
            />
          }
        />
      )}
    </Page>
  );
}

export function ZonesTable({
  framed = true,
  recordSets,
  zones,
  onOpenZone,
}: {
  framed?: boolean;
  recordSets: RecordSet[];
  zones: Zone[];
  onOpenZone: (zone: Zone) => void;
}) {
  return (
    <DataGridTable
      columns="minmax(190px,1.15fr) minmax(150px,0.9fr) minmax(170px,1fr) minmax(180px,1fr) minmax(110px,0.55fr) minmax(90px,0.45fr)"
      framed={framed}
    >
      <GridHeader
        labels={[
          { label: 'Domain' },
          { label: 'Namespace' },
          { label: 'Zone class', hideSmall: true },
          { label: 'Conditions' },
          { label: 'Age', hideSmall: true },
          { label: 'Records' },
        ]}
      />
      {zones.map(zone => {
        const recordCount = recordSets.filter(
          recordSet =>
            zoneRefNamespace(recordSet) === namespaceOf(zone) &&
            recordSet.spec.zoneRef.name === nameOf(zone)
        ).length;
        return (
          <GridRow key={resourceKey(zone)}>
            <GridCell>
              <Stack spacing={0.25} sx={{ minWidth: 0 }}>
                <DesignLinkButton onClick={() => onOpenZone(zone)}>
                  {zone.spec.domainName}
                </DesignLinkButton>
                <Typography sx={{ color: ui.faint, fontSize: 12 }}>{nameOf(zone)}</Typography>
              </Stack>
            </GridCell>
            <GridCell>
              <Typography sx={{ color: ui.muted, fontSize: 14 }} noWrap>
                {namespaceOf(zone)}
              </Typography>
            </GridCell>
            <GridCell hideSmall>
              <Typography sx={{ color: ui.muted, fontSize: 14 }} noWrap>
                {zoneClassRefNamespace(zone)}/{zone.spec.zoneClassRef.name}
              </Typography>
            </GridCell>
            <GridCell>
              <Conditions conditions={zone.status?.conditions} />
            </GridCell>
            <GridCell hideSmall>
              <Typography sx={{ color: ui.muted, fontSize: 14 }}>{ageOf(zone)}</Typography>
            </GridCell>
            <GridCell>
              <Typography sx={{ color: ui.text, fontSize: 14 }}>{recordCount}</Typography>
            </GridCell>
          </GridRow>
        );
      })}
    </DataGridTable>
  );
}
