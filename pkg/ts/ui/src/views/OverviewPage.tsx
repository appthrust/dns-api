/** @jsxRuntime classic */
import { Box } from '../components/primitives';
import { Typography } from '../components/primitives';
import React from 'react';
import { useDnsData } from '../api/dns';
import {
  conditionOf,
  isBadCondition,
  nameOf,
  namespaceOf,
  resourceKey,
  zoneRefNamespace,
} from '../resources';
import type { DnsCondition } from '../types/resources';
import {
  Conditions,
  DesignLinkButton,
  EmptyState,
  Page,
  Panel,
  SummaryTile,
  ui,
} from './common/ui';
import { integrationDetailPath } from './platform/routes';
import { recordSetPath, useZonesNavigation, zonePath } from './zones/routes';

type DnsAlert = {
  resource: string;
  condition: DnsCondition;
  onClick?: () => void;
};

export function OverviewPage() {
  const data = useDnsData();
  const navigation = useZonesNavigation();
  const alerts: DnsAlert[] = [
    ...data.zones.items.flatMap(
      zone =>
        zone.status?.conditions?.filter(isBadCondition).map(condition => ({
          resource: `Zone ${resourceKey(zone)}`,
          condition,
          onClick: () => navigation.push(zonePath(zone)),
        })) ?? []
    ),
    ...data.recordSets.items.flatMap(
      recordSet => {
        const zone = data.zones.items.find(
          item =>
            namespaceOf(item) === zoneRefNamespace(recordSet) &&
            nameOf(item) === recordSet.spec.zoneRef.name
        );
        return (
          recordSet.status?.conditions?.filter(isBadCondition).map(condition => ({
            resource: `RecordSet ${resourceKey(recordSet)}`,
            condition,
            onClick: zone ? () => navigation.push(recordSetPath(zone, recordSet)) : undefined,
          })) ?? []
        );
      }
    ),
    ...data.identities.items.flatMap(
      identity =>
        identity.status?.conditions?.filter(isBadCondition).map(condition => ({
          resource: `${identity.kind} ${resourceKey(identity)}`,
          condition,
          onClick: () => navigation.push(integrationDetailPath(identity)),
        })) ?? []
    ),
  ];
  const activeConflicts = data.recordSets.items.filter(
    recordSet => conditionOf(recordSet, 'Accepted')?.reason === 'RecordSetConflict'
  ).length;

  return (
    <Page
      title="Overview"
      description="A compact landing page for DNS API operational status. Platform setup is separated into its own workflow."
    >
      <Box
        sx={{
          display: 'grid',
          gap: 2,
          gridTemplateColumns: { xs: '1fr', sm: '1fr 1fr', lg: 'repeat(4, 1fr)' },
        }}
      >
        <SummaryTile
          label="Zones"
          value={data.zones.items.length}
          icon="mdi:earth"
          sub={`${
            data.zones.items.filter(zone => zone.status?.conditions?.some(isBadCondition)).length
          } need attention`}
        />
        <SummaryTile
          label="RecordSets"
          value={data.recordSets.items.length}
          icon="mdi:lan"
          sub={`${activeConflicts} conflicts`}
        />
        <SummaryTile
          label="Provider Identities"
          value={data.identities.items.length}
          icon="mdi:key-chain"
          sub={`${
            data.identities.items.filter(identity =>
              identity.status?.conditions?.some(isBadCondition)
            ).length
          } not ready`}
        />
        <SummaryTile
          label="ZoneClasses"
          value={data.zoneClasses.items.length}
          icon="mdi:layers"
          sub={`${
            data.zoneClasses.items.filter(zoneClass =>
              zoneClass.status?.conditions?.some(isBadCondition)
            ).length
          } not ready`}
        />
      </Box>
      <Panel sx={{ overflow: 'hidden' }}>
        <Box sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}>
          <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
            Active DNS alerts
          </Typography>
        </Box>
        {alerts.length ? (
          <Box>
            {alerts.slice(0, 8).map(alert => (
              <Box
                key={`${alert.resource}-${alert.condition.type}`}
                sx={{
                  borderBottom: 1,
                  borderColor: ui.borderSoft,
                  display: 'grid',
                  gap: 2,
                  gridTemplateColumns: {
                    xs: '1fr',
                    md: 'minmax(220px,0.9fr) minmax(150px,0.45fr) minmax(160px,0.55fr) minmax(240px,1fr)',
                  },
                  px: 2,
                  py: 1.5,
                }}
              >
                {alert.onClick ? (
                  <DesignLinkButton onClick={alert.onClick}>{alert.resource}</DesignLinkButton>
                ) : (
                  <Typography sx={{ color: ui.accent, fontSize: 14, fontWeight: 600 }}>
                    {alert.resource}
                  </Typography>
                )}
                <Conditions conditions={[alert.condition]} />
                <Typography sx={{ color: ui.muted, fontSize: 14 }}>
                  {alert.condition.reason || '-'}
                </Typography>
                <Typography sx={{ color: ui.faint, fontSize: 14 }}>
                  {alert.condition.message || '-'}
                </Typography>
              </Box>
            ))}
          </Box>
        ) : (
          <EmptyState
            title="No active DNS alerts"
            body="Visible Zones, RecordSets, and Provider Identities do not currently report failing conditions."
          />
        )}
      </Panel>
    </Page>
  );
}
