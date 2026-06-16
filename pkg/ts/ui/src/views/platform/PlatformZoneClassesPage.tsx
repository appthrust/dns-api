/** @jsxRuntime classic */
import { Typography } from '../../components/primitives';
import React from 'react';
import { useDnsData } from '../../api/dns';
import { nameOf, namespaceOf, resourceKey, zoneClassIdentityName } from '../../resources';
import {
  ageOf,
  Conditions,
  DataGridTable,
  DesignLinkButton,
  EmptyState,
  GridCell,
  GridHeader,
  GridRow,
  ProviderBadge,
  providerForResource,
  ToolbarButton,
  ui,
} from '../common/ui';
import { useZoneClassAccess, zonesReferencingZoneClass } from './resources';
import {
  PLATFORM_INTEGRATIONS_PATH,
  PLATFORM_ZONE_CLASS_NEW_PATH,
  PlatformContentTitle,
  PlatformRouteFrame,
  usePlatformNavigation,
  zoneClassDetailPath,
} from './routes';

export function PlatformZoneClassesPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const { canCreateZoneClass } = useZoneClassAccess();
  const hasIdentities = data.identities.items.length > 0;

  return (
    <PlatformRouteFrame active="zoneclasses">
      <PlatformContentTitle
        title="Zone Classes"
        description="ZoneClasses publish provider-backed DNS zone capability to selected namespaces."
        action={
          hasIdentities ? (
            <ToolbarButton
              label="New ZoneClass"
              icon="mdi:plus"
              disabled={!canCreateZoneClass}
              onClick={() => navigation.push(PLATFORM_ZONE_CLASS_NEW_PATH)}
            />
          ) : (
            <ToolbarButton
              label="Go to Provider Identities"
              icon="mdi:arrow-right"
              tone="secondary"
              onClick={() => navigation.push(PLATFORM_INTEGRATIONS_PATH)}
            />
          )
        }
      />
      {!hasIdentities ? (
        <EmptyState
          title="No Provider Identities"
          body="Create a Provider Identity before creating ZoneClasses."
          action={
            <ToolbarButton
              label="Go to Provider Identities"
              icon="mdi:arrow-right"
              tone="secondary"
              onClick={() => navigation.push(PLATFORM_INTEGRATIONS_PATH)}
            />
          }
        />
      ) : data.zoneClasses.items.length ? (
        <DataGridTable columns="minmax(168px,1.05fr) minmax(150px,0.95fr) minmax(132px,0.9fr) minmax(168px,1fr) minmax(160px,0.9fr) minmax(100px,0.55fr) minmax(90px,0.5fr)">
          <GridHeader
            labels={[
              { label: 'Name' },
              { label: 'Namespace', hideSmall: true },
              { label: 'Provider' },
              { label: 'Provider Identity' },
              { label: 'Conditions' },
              { label: 'Age', hideSmall: true },
              { label: 'Zones', hideSmall: true },
            ]}
          />
          {data.zoneClasses.items.map(zoneClass => {
            const referencingZones = zonesReferencingZoneClass(data.zones.items, zoneClass);
            return (
              <GridRow key={resourceKey(zoneClass)}>
                <GridCell>
                  <DesignLinkButton onClick={() => navigation.push(zoneClassDetailPath(zoneClass))}>
                    {nameOf(zoneClass)}
                  </DesignLinkButton>
                </GridCell>
                <GridCell hideSmall>
                  <Typography sx={{ color: ui.muted, fontSize: 14 }} noWrap>
                    {namespaceOf(zoneClass)}
                  </Typography>
                </GridCell>
                <GridCell>
                  <ProviderBadge provider={providerForResource(zoneClass)} />
                </GridCell>
                <GridCell>
                  <Typography sx={{ color: ui.text, fontSize: 14 }} noWrap>
                    {zoneClassIdentityName(zoneClass) || '-'}
                  </Typography>
                </GridCell>
                <GridCell>
                  <Conditions conditions={zoneClass.status?.conditions} />
                </GridCell>
                <GridCell hideSmall>
                  <Typography sx={{ color: ui.muted, fontSize: 14 }}>{ageOf(zoneClass)}</Typography>
                </GridCell>
                <GridCell hideSmall>
                  <Typography sx={{ color: ui.muted, fontSize: 14 }}>
                    {referencingZones.length}
                  </Typography>
                </GridCell>
              </GridRow>
            );
          })}
        </DataGridTable>
      ) : (
        <EmptyState
          title="No ZoneClasses"
          body="Create a ZoneClass to publish DNS zone capability to application namespaces."
          action={
            <ToolbarButton
              label="New ZoneClass"
              icon="mdi:plus"
              disabled={!canCreateZoneClass}
              onClick={() => navigation.push(PLATFORM_ZONE_CLASS_NEW_PATH)}
            />
          }
        />
      )}
    </PlatformRouteFrame>
  );
}
