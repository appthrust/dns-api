/** @jsxRuntime classic */
import { Box } from '../../components/primitives';
import { Button } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import React from 'react';
import { nameOf, namespaceOf, resourceKey, zoneClassIdentityName } from '../../resources';
import type { ZoneClass } from '../../types/resources';
import {
  type BreadcrumbItem,
  Conditions,
  descriptionOf,
  EmptyState,
  Page,
  ProviderBadge,
  providerForResource,
  selectedBg,
  tableHeaderSx,
  ToolbarButton,
  ui,
} from '../common/ui';

export function ZoneClassPickerForZonePage({
  zoneClasses,
  onCancel,
  onSelect,
  breadcrumb,
}: {
  zoneClasses: ZoneClass[];
  onCancel: () => void;
  onSelect: (zoneClass: ZoneClass) => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const grouped = zoneClasses.reduce<Record<string, ZoneClass[]>>((groups, zoneClass) => {
    const provider = providerForResource(zoneClass);
    groups[provider] = groups[provider] ?? [];
    groups[provider].push(zoneClass);
    return groups;
  }, {});

  return (
    <Page
      breadcrumb={breadcrumb}
      title="Select ZoneClass"
      description="Choose the provider policy that will create or adopt this public DNS zone."
      actions={
        <ToolbarButton
          label="Cancel"
          icon="mdi:close"
          tone="secondary"
          onClick={onCancel}
        />
      }
    >
      {zoneClasses.length ? (
        <Stack spacing={3}>
          {Object.entries(grouped).map(([provider, items]) => (
            <Box key={provider}>
              <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1.5 }}>
                <ProviderBadge provider={provider} />
                <Typography sx={{ color: ui.faint, fontSize: 12 }}>
                  Public DNS ZoneClasses
                </Typography>
              </Stack>
              <Box
                sx={{
                  display: 'grid',
                  gap: 2,
                  gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)', lg: 'repeat(3, 1fr)' },
                }}
              >
                {items.map(zoneClass => (
                  <Button
                    key={resourceKey(zoneClass)}
                    variant="outlined"
                    onClick={() => onSelect(zoneClass)}
                    sx={{
                      alignItems: 'stretch',
                      bgcolor: ui.panelBgSoft,
                      borderRadius: 1,
                      color: ui.text,
                      justifyContent: 'flex-start',
                      minHeight: 204,
                      p: 2,
                      textAlign: 'left',
                      textTransform: 'none',
                      '&:hover': {
                        bgcolor: selectedBg,
                        borderColor: ui.accent,
                      },
                    }}
                  >
                    <Stack spacing={1.25} sx={{ width: '100%' }}>
                      <Stack
                        direction="row"
                        spacing={1.5}
                        alignItems="center"
                        justifyContent="space-between"
                        useFlexGap
                      >
                        <ProviderBadge provider={provider} />
                        <Conditions conditions={zoneClass.status?.conditions} />
                      </Stack>
                      <Box>
                        <Typography sx={{ color: ui.text, fontSize: 16, fontWeight: 600 }}>
                          {nameOf(zoneClass)}
                        </Typography>
                        <Typography sx={{ color: ui.faint, fontSize: 13, mt: 0.5 }}>
                          {namespaceOf(zoneClass)}
                        </Typography>
                      </Box>
                      <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.6 }}>
                        {descriptionOf(zoneClass) ||
                          'Public DNS only. Private DNS and VPC selection are not part of this flow.'}
                      </Typography>
                      <Box sx={{ mt: 'auto' }}>
                        <Typography sx={tableHeaderSx}>Provider Identity</Typography>
                        <Typography sx={{ color: ui.text, fontSize: 13 }}>
                          {zoneClassIdentityName(zoneClass) || '-'}
                        </Typography>
                      </Box>
                    </Stack>
                  </Button>
                ))}
              </Box>
            </Box>
          ))}
        </Stack>
      ) : (
        <EmptyState
          title="No ZoneClasses"
          body="Create a Provider Identity and ZoneClass before creating Zones."
        />
      )}
    </Page>
  );
}
