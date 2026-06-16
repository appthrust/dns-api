/** @jsxRuntime classic */
import { Stack, Typography } from '../../components/primitives';
import React from 'react';
import { useDnsData } from '../../api/dns';
import { nameOf, namespaceOf, resourceKey } from '../../resources';
import type { ProviderIdentity } from '../../types/resources';
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
import { useIdentityAccess, zoneClassesReferencingIdentity } from './resources';
import {
  integrationDetailPath,
  PLATFORM_INTEGRATION_NEW_PATH,
  PlatformContentTitle,
  PlatformRouteFrame,
  usePlatformNavigation,
} from './routes';

export function PlatformIntegrationsPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const { canCreateIdentity } = useIdentityAccess();

  return (
    <PlatformRouteFrame active="integrations">
      <PlatformContentTitle
        title="Provider Identities"
        description="Provider Identities define how DNS API connects to DNS providers. ZoneClasses use them to create and manage zones."
        action={
          <ToolbarButton
            label="New Provider Identity"
            icon="mdi:plus"
            disabled={!canCreateIdentity}
            onClick={() => navigation.push(PLATFORM_INTEGRATION_NEW_PATH)}
          />
        }
      />
      {data.identities.items.length ? (
        <DataGridTable columns="minmax(168px,1fr) minmax(130px,0.75fr) minmax(260px,1.25fr) minmax(150px,0.8fr) minmax(160px,0.85fr) minmax(90px,0.5fr) minmax(110px,0.6fr)">
          <GridHeader
            labels={[
              { label: 'Name' },
              { label: 'Namespace', hideSmall: true },
              { label: 'Provider' },
              { label: 'Kind' },
              { label: 'Conditions' },
              { label: 'Age', hideSmall: true },
              { label: 'Used by', hideSmall: true },
            ]}
          />
          {data.identities.items.map(identity => {
            const referencingZoneClasses = zoneClassesReferencingIdentity(
              data.zoneClasses.items,
              identity
            );
            const accountID = providerIdentityAccountID(identity);
            return (
              <GridRow key={resourceKey(identity)}>
                <GridCell>
                  <DesignLinkButton
                    onClick={() => navigation.push(integrationDetailPath(identity))}
                  >
                    {nameOf(identity)}
                  </DesignLinkButton>
                </GridCell>
                <GridCell hideSmall>
                  <Typography sx={{ color: ui.muted, fontSize: 14 }} noWrap>
                    {namespaceOf(identity)}
                  </Typography>
                </GridCell>
                <GridCell>
                  <Stack spacing={0.5} alignItems="flex-start">
                    <ProviderBadge provider={providerForResource(identity)} />
                    <Typography
                      title={accountID}
                      noWrap
                      sx={{
                        color: accountID ? ui.muted : ui.faint,
                        fontFamily: accountID ? 'monospace' : 'inherit',
                        fontSize: 12,
                        lineHeight: 1.4,
                        maxWidth: '100%',
                      }}
                    >
                      {accountID ?? 'Account ID pending'}
                    </Typography>
                  </Stack>
                </GridCell>
                <GridCell>
                  <Typography sx={{ color: ui.text, fontSize: 14 }}>{identity.kind}</Typography>
                </GridCell>
                <GridCell>
                  <Conditions conditions={identity.status?.conditions} />
                </GridCell>
                <GridCell hideSmall>
                  <Typography sx={{ color: ui.muted, fontSize: 14 }}>{ageOf(identity)}</Typography>
                </GridCell>
                <GridCell hideSmall>
                  <Typography sx={{ color: ui.muted, fontSize: 14 }}>
                    {referencingZoneClasses.length} ZoneClass
                  </Typography>
                </GridCell>
              </GridRow>
            );
          })}
        </DataGridTable>
      ) : (
        <EmptyState
          title="No Provider Identities"
          body="Create a Provider Identity before publishing provider capability with ZoneClass."
          action={
            <ToolbarButton
              label="New Provider Identity"
              icon="mdi:plus"
              disabled={!canCreateIdentity}
              onClick={() => navigation.push(PLATFORM_INTEGRATION_NEW_PATH)}
            />
          }
        />
      )}
    </PlatformRouteFrame>
  );
}

function providerIdentityAccountID(identity: ProviderIdentity): string | undefined {
  if (identity.kind === 'CloudflareIdentity') {
    return identity.status?.account?.id;
  }
  return identity.spec.accountID;
}
