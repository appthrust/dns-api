/** @jsxRuntime classic */
import React from 'react';
import { deleteByKey, useAccess, useDnsData } from '../../api/dns';
import { nameOf, namespaceOf, ZoneResource, zoneClassRefNamespace, zoneRefNamespace } from '../../resources';
import {
  DangerPanel,
  DetailFieldGrid,
  EmptyState,
  Page,
  Panel,
  tableHeaderSx,
  ToolbarButton,
  ui,
  useNotice,
} from '../common/ui';
import { findZone, findZoneClass, unresolvedBreadcrumb, useZoneRouteParams, useZonesNavigation, zoneBreadcrumb, zonePath, ZONES_BREADCRUMB, ZONES_PATH } from './routes';
import { Box } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import { Conditions } from '../common/ui';
import { providerForResource } from '../common/ui';

export function ZoneDeletePage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  const params = useZoneRouteParams();
  const { showError, snackbar } = useNotice();
  const canDelete = useAccess(ZoneResource, 'delete', {
    group: 'dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'zones',
  });
  const zone = findZone(data.zones.items, params.namespace, params.name);

  if (!zone) {
    return (
      <Page
        breadcrumb={[ZONES_BREADCRUMB, unresolvedBreadcrumb(params.name, data.zones.loading)]}
        title="Zone not found"
        description="The requested Zone is not visible in this cluster."
      >
        <EmptyState
          title="Zone not found"
          body="The requested Zone is not visible in this cluster."
        />
      </Page>
    );
  }

  const zoneClass = findZoneClass(
    data.zoneClasses.items,
    zoneClassRefNamespace(zone),
    zone.spec.zoneClassRef.name
  );
  const provider = providerForResource(zoneClass);
  const hostedZoneID =
    zone.status?.provider?.data?.hostedZoneID ||
    (zone.spec.adoption?.hostedZoneId as string | undefined) ||
    (zone.spec.adoption?.zoneID as string | undefined) ||
    '-';
  const providerZoneIDLabel = provider === 'Cloudflare' ? 'Zone ID' : 'Hosted zone ID';
  const zoneDeletionPolicy = zoneClass?.spec.parameters.zoneDeletionPolicy ?? 'Retain';
  const referencingRecordSets = data.recordSets.items.filter(
    recordSet =>
      zoneRefNamespace(recordSet) === namespaceOf(zone) && recordSet.spec.zoneRef.name === nameOf(zone)
  );
  const blocked = !canDelete || referencingRecordSets.length > 0;
  const blockedMessage = !canDelete
    ? 'Your current RBAC permissions do not allow deleting this Zone.'
    : 'Delete or move remaining RecordSets before deleting this Zone.';
  const providerEffect =
    zoneDeletionPolicy === 'Delete'
      ? 'Provider hosted zone will be deleted by the controller.'
      : 'Provider hosted zone will be retained.';

  async function confirmDelete() {
    try {
      await deleteByKey(data.zones.objects, namespaceOf(zone), nameOf(zone));
      navigation.push(ZONES_PATH);
    } catch (error) {
      showError(error);
    }
  }

  return (
    <Page
      breadcrumb={[ZONES_BREADCRUMB, zoneBreadcrumb(zone)]}
      title="Delete Zone"
      description={`Review provider effects before deleting ${namespaceOf(zone)}/${nameOf(zone)}.`}
      actions={
        <ToolbarButton
          label="Cancel"
          icon="mdi:close"
          tone="secondary"
          onClick={() => navigation.push(zonePath(zone))}
        />
      }
    >
      <Stack spacing={2.5}>
        <DetailFieldGrid
          fields={[
            ['Target', `${namespaceOf(zone)}/${nameOf(zone)}`],
            ['Domain', zone.spec.domainName],
            [
              'ZoneClass',
              zoneClass ? `${namespaceOf(zoneClass)}/${nameOf(zoneClass)}` : 'Not visible',
            ],
            ['Provider', provider],
            [providerZoneIDLabel, hostedZoneID],
            ['Zone deletion policy', zoneDeletionPolicy],
            ['Remaining RecordSets', referencingRecordSets.length.toString()],
            ['Provider effect', providerEffect],
          ]}
        />

        {referencingRecordSets.length ? (
          <Panel sx={{ bgcolor: ui.warningBgSoft, borderColor: ui.warningBorder, p: 2 }}>
            <Stack spacing={1.5}>
              <Typography sx={{ color: ui.warningText, fontSize: 14, fontWeight: 700 }}>
                Delete blocked by RecordSets
              </Typography>
              <Typography sx={{ color: ui.warningText, fontSize: 14, lineHeight: 1.65 }}>
                The controller should not delete a Zone while RecordSets still reference it.
              </Typography>
              <Box sx={{ display: 'grid', gap: 1 }}>
                {referencingRecordSets.map(recordSet => (
                  <Box key={`${namespaceOf(recordSet)}/${nameOf(recordSet)}`}>
                    <Typography sx={tableHeaderSx}>
                      {namespaceOf(recordSet)}/{nameOf(recordSet)}
                    </Typography>
                    <Conditions conditions={recordSet.status?.conditions} />
                  </Box>
                ))}
              </Box>
            </Stack>
          </Panel>
        ) : null}

        <DangerPanel
          blocked={blocked}
          blockedMessage={blockedMessage}
          readyMessage={providerEffect}
          actionLabel="Delete Zone"
          onAction={confirmDelete}
        />
      </Stack>
      {snackbar}
    </Page>
  );
}
