/** @jsxRuntime classic */
import { Icon } from '../../components/Icon';
import { Box } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import React from 'react';
import { deleteByKey, useDnsData } from '../../api/dns';
import { nameOf, namespaceOf } from '../../resources';
import { Panel, tableHeaderSx, ToolbarButton, ui, useNotice } from '../common/ui';
import { findZoneClass, useZoneClassAccess, zonesReferencingZoneClass } from './resources';
import {
  PLATFORM_ZONE_CLASSES_PATH,
  PlatformContentTitle,
  PlatformMissingResourcePage,
  PlatformRouteFrame,
  unresolvedBreadcrumb,
  usePlatformNavigation,
  useResourceRouteParams,
  ZONE_CLASSES_BREADCRUMB,
  zoneClassBreadcrumb,
  zoneClassDetailPath,
} from './routes';

export function PlatformZoneClassDeletePage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const params = useResourceRouteParams();
  const { canDeleteZoneClass } = useZoneClassAccess();
  const { showError, snackbar } = useNotice();
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

  const referencingZones = zonesReferencingZoneClass(data.zones.items, zoneClass);
  const blocked = referencingZones.length > 0 || !canDeleteZoneClass;
  const blockedMessage = canDeleteZoneClass
    ? `This ZoneClass is used by ${referencingZones.length} Zone resource${
        referencingZones.length === 1 ? '' : 's'
      }. Move or delete those Zones first.`
    : 'You do not have permission to delete ZoneClass resources.';

  async function confirmDelete() {
    try {
      await deleteByKey(data.zoneClasses.objects, namespaceOf(zoneClass), nameOf(zoneClass));
      navigation.push(PLATFORM_ZONE_CLASSES_PATH);
    } catch (error) {
      showError(error);
    }
  }

  return (
    <PlatformRouteFrame active="zoneclasses">
      <Stack spacing={2.5}>
        <PlatformContentTitle
          breadcrumb={[ZONE_CLASSES_BREADCRUMB, zoneClassBreadcrumb(zoneClass)]}
          title="Delete ZoneClass"
          description={`Review references before deleting ${namespaceOf(zoneClass)}/${nameOf(zoneClass)}.`}
          action={
            <ToolbarButton
              label="Cancel"
              icon="mdi:close"
              tone="secondary"
              onClick={() => navigation.push(zoneClassDetailPath(zoneClass))}
            />
          }
        />
        <Panel sx={{ maxWidth: 960, overflow: 'hidden' }}>
          <Box sx={{ borderBottom: 1, borderColor: ui.border, p: 2.5 }}>
            <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
              Delete confirmation
            </Typography>
            <Typography sx={{ color: ui.faint, fontSize: 14, lineHeight: 1.65, mt: 1 }}>
              Deleting a ZoneClass removes only the Kubernetes policy resource. It does not delete
              provider hosted zones or DNS records.
            </Typography>
          </Box>
          <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' } }}>
            {[
              ['ZoneClass', `${namespaceOf(zoneClass)}/${nameOf(zoneClass)}`],
              ['Provider', `${zoneClass.spec.provider.name}/${zoneClass.spec.provider.version}`],
              ['Zones', String(referencingZones.length)],
              ['Delete allowed', blocked ? 'No' : 'Yes'],
            ].map(([label, value], index) => (
              <Box
                key={label}
                sx={{
                  borderBottom: 1,
                  borderBottomColor: ui.borderSoft,
                  borderRight: { md: index % 2 === 0 ? 1 : 0 },
                  borderRightColor: ui.borderSoft,
                  px: 2.5,
                  py: 2,
                }}
              >
                <Typography sx={tableHeaderSx}>{label}</Typography>
                <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mt: 1 }}>
                  {value}
                </Typography>
              </Box>
            ))}
          </Box>
          <Box
            sx={{
              bgcolor: blocked ? ui.warningBgSoft : ui.dangerBg,
              border: 1,
              borderColor: blocked ? ui.warningBorder : ui.dangerBorder,
              borderRadius: 1,
              m: 2.5,
              p: 2,
            }}
          >
            <Stack direction="row" spacing={1.5} alignItems="flex-start">
              <Icon icon={blocked ? 'mdi:alert-outline' : 'mdi:alert-circle-outline'} width={20} />
              <Box>
                <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                  {blocked
                    ? 'This ZoneClass cannot be deleted yet.'
                    : 'This ZoneClass will be deleted.'}
                </Typography>
                <Typography sx={{ color: ui.muted, fontSize: 14, lineHeight: 1.65, mt: 1 }}>
                  {blocked
                    ? blockedMessage
                    : 'Zones will no longer be able to reference this policy after deletion.'}
                </Typography>
              </Box>
            </Stack>
          </Box>
          <Stack
            direction="row"
            justifyContent="flex-end"
            spacing={1}
            sx={{ borderTop: 1, borderColor: ui.border, px: 2.5, py: 2 }}
          >
            <ToolbarButton
              label="Cancel"
              icon="mdi:close"
              tone="secondary"
              onClick={() => navigation.push(zoneClassDetailPath(zoneClass))}
            />
            <ToolbarButton
              label="Delete ZoneClass"
              icon="mdi:delete"
              tone="danger"
              disabled={blocked}
              onClick={confirmDelete}
            />
          </Stack>
        </Panel>
        {snackbar}
      </Stack>
    </PlatformRouteFrame>
  );
}
