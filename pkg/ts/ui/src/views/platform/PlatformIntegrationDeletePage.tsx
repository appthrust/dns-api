/** @jsxRuntime classic */
import { Icon } from '../../components/Icon';
import { Box } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import React from 'react';
import { deleteByKey, useDnsData } from '../../api/dns';
import { nameOf, namespaceOf } from '../../resources';
import {
  Panel,
  providerForResource,
  tableHeaderSx,
  ToolbarButton,
  ui,
  useNotice,
} from '../common/ui';
import { findIdentity, useIdentityAccess, zoneClassesReferencingIdentity } from './resources';
import {
  INTEGRATIONS_BREADCRUMB,
  integrationBreadcrumb,
  integrationDetailPath,
  PLATFORM_INTEGRATIONS_PATH,
  PlatformContentTitle,
  PlatformMissingResourcePage,
  PlatformRouteFrame,
  unresolvedBreadcrumb,
  usePlatformNavigation,
  useResourceRouteParams,
} from './routes';

export function PlatformIntegrationDeletePage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const params = useResourceRouteParams();
  const { canDeleteIdentity } = useIdentityAccess();
  const { showError, snackbar } = useNotice();
  const identity = findIdentity(data.identities.items, params.namespace, params.name);
  const identityCrumb = identity
    ? integrationBreadcrumb(identity)
    : unresolvedBreadcrumb(params.name, data.identities.loading);

  if (!identity) {
    return (
      <PlatformMissingResourcePage
        active="integrations"
        breadcrumb={[INTEGRATIONS_BREADCRUMB, identityCrumb]}
        title="Provider Identity not found"
        body="The requested Provider Identity is not visible in this cluster."
      />
    );
  }

  const referencingZoneClasses = zoneClassesReferencingIdentity(data.zoneClasses.items, identity);
  const provider = providerForResource(identity);
  const scope =
    identity.kind === 'CloudflareIdentity'
      ? identity.status?.account?.id ?? '-'
      : identity.spec.accountID;
  const blocked = referencingZoneClasses.length > 0 || !canDeleteIdentity;
  const blockedMessage = canDeleteIdentity
    ? `This Provider Identity is used by ${referencingZoneClasses.length} ZoneClass resource${
        referencingZoneClasses.length === 1 ? '' : 's'
      }. Move or delete those ZoneClasses first.`
    : 'You do not have permission to delete Route53Identity resources.';

  async function confirmDelete() {
    try {
      await deleteByKey(data.identities.objects, namespaceOf(identity), nameOf(identity));
      navigation.push(PLATFORM_INTEGRATIONS_PATH);
    } catch (error) {
      showError(error);
    }
  }

  return (
    <PlatformRouteFrame active="integrations">
      <Stack spacing={2.5}>
        <PlatformContentTitle
          breadcrumb={[INTEGRATIONS_BREADCRUMB, integrationBreadcrumb(identity)]}
          title="Delete Provider Identity"
          description={`Review references before deleting ${namespaceOf(identity)}/${nameOf(identity)}.`}
          action={
            <ToolbarButton
              label="Cancel"
              icon="mdi:close"
              tone="secondary"
              onClick={() => navigation.push(integrationDetailPath(identity))}
            />
          }
        />
        <Panel sx={{ maxWidth: 960, overflow: 'hidden' }}>
          <Box sx={{ borderBottom: 1, borderColor: ui.border, p: 2.5 }}>
            <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
              Delete confirmation
            </Typography>
            <Typography sx={{ color: ui.faint, fontSize: 14, lineHeight: 1.65, mt: 1 }}>
              Deleting a Provider Identity removes only the Kubernetes identity resource. It does not
              delete IAM roles, cloud accounts, hosted zones, or DNS records.
            </Typography>
          </Box>
          <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' } }}>
            {[
              ['Provider Identity', `${namespaceOf(identity)}/${nameOf(identity)}`],
              ['Provider', provider],
              ['Kind', identity.kind],
              ['Scope', scope],
              ['Used by', `${referencingZoneClasses.length} ZoneClass`],
              ['Delete allowed', blocked ? 'No' : 'Yes'],
            ].map(([label, value], index) => (
              <Box
                key={label}
                sx={{
                  borderBottom: 1,
                  borderColor: ui.borderSoft,
                  borderRight: { md: index % 2 === 0 ? 1 : 0 },
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
                    ? 'This Provider Identity cannot be deleted yet.'
                    : 'This Provider Identity will be deleted.'}
                </Typography>
                <Typography sx={{ color: ui.muted, fontSize: 14, lineHeight: 1.65, mt: 1 }}>
                  {blocked
                    ? blockedMessage
                    : 'ZoneClasses will no longer be able to reference this identity after deletion.'}
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
              onClick={() => navigation.push(integrationDetailPath(identity))}
            />
            <ToolbarButton
              label="Delete Provider Identity"
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
