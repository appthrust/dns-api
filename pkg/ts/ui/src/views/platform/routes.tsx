/** @jsxRuntime classic */
import { Box } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import React from 'react';
import { useParams } from 'react-router-dom';
import { useDnsPlatform } from '../../platform';
import { nameOf, namespaceOf } from '../../resources';
import type { ProviderIdentity, ZoneClass } from '../../types/resources';
import { type BreadcrumbItem, EmptyState, PageHeader, ToolbarButton, ui } from '../common/ui';

export type PlatformSection = 'integrations' | 'zoneclasses';

export const PLATFORM_INTEGRATIONS_PATH = '/dns/platform/integrations';
export const PLATFORM_INTEGRATION_NEW_PATH = '/dns/platform/integrations/new';
export const PLATFORM_ROUTE53_INTEGRATION_NEW_PATH = '/dns/platform/integrations/new/route53';
export const PLATFORM_CLOUDFLARE_INTEGRATION_NEW_PATH =
  '/dns/platform/integrations/new/cloudflare';
export const PLATFORM_ZONE_CLASSES_PATH = '/dns/platform/zoneclasses';
export const PLATFORM_ZONE_CLASS_NEW_PATH = '/dns/platform/zoneclasses/new';

type ResourceRouteParams = {
  namespace?: string;
  name?: string;
};

function pathPart(value: string): string {
  return encodeURIComponent(value);
}

export function routeParam(value?: string): string {
  return value ? decodeURIComponent(value) : '';
}

export function usePlatformNavigation() {
  const platform = useDnsPlatform();
  return React.useMemo(
    () => ({
      push: platform.navigation.push,
      replace: platform.navigation.replace,
    }),
    [platform]
  );
}

export function integrationDetailPath(identity: ProviderIdentity): string {
  return `${PLATFORM_INTEGRATIONS_PATH}/${pathPart(namespaceOf(identity))}/${pathPart(
    nameOf(identity)
  )}`;
}

export function integrationEditPath(identity: ProviderIdentity): string {
  return `${integrationDetailPath(identity)}/edit`;
}

export function integrationDeletePath(identity: ProviderIdentity): string {
  return `${integrationDetailPath(identity)}/delete`;
}

export function zoneClassDetailPath(zoneClass: ZoneClass): string {
  return `${PLATFORM_ZONE_CLASSES_PATH}/${pathPart(namespaceOf(zoneClass))}/${pathPart(
    nameOf(zoneClass)
  )}`;
}

export function zoneClassEditPath(zoneClass: ZoneClass): string {
  return `${zoneClassDetailPath(zoneClass)}/edit`;
}

export function zoneClassDeletePath(zoneClass: ZoneClass): string {
  return `${zoneClassDetailPath(zoneClass)}/delete`;
}

export function zoneClassNewPath(identity: ProviderIdentity): string {
  return `${PLATFORM_ZONE_CLASS_NEW_PATH}/${pathPart(namespaceOf(identity))}/${pathPart(
    nameOf(identity)
  )}`;
}

export const ZONE_CLASSES_BREADCRUMB: BreadcrumbItem = {
  label: 'Zone Classes',
  path: PLATFORM_ZONE_CLASSES_PATH,
};

export const INTEGRATIONS_BREADCRUMB: BreadcrumbItem = {
  label: 'Provider Identities',
  path: PLATFORM_INTEGRATIONS_PATH,
};

export function zoneClassBreadcrumb(zoneClass: ZoneClass): BreadcrumbItem {
  return { label: nameOf(zoneClass), path: zoneClassDetailPath(zoneClass) };
}

export function integrationBreadcrumb(identity: ProviderIdentity): BreadcrumbItem {
  return { label: nameOf(identity), path: integrationDetailPath(identity) };
}

export function PlatformContentTitle({
  title,
  description,
  action,
  breadcrumb,
}: {
  title: string;
  description: string;
  action?: React.ReactNode;
  breadcrumb?: BreadcrumbItem[];
}) {
  return (
    <Box sx={{ mb: 2.5 }}>
      <PageHeader breadcrumbs={breadcrumb} title={title} description={description} actions={action} />
    </Box>
  );
}

export function useResourceRouteParams(): { namespace: string; name: string } {
  const params = useParams<ResourceRouteParams>();
  return {
    namespace: routeParam(params.namespace),
    name: routeParam(params.name),
  };
}

export function PlatformRouteFrame({
  children,
}: {
  active: PlatformSection;
  children: React.ReactNode;
}) {
  return (
    <Box
      sx={{
        bgcolor: ui.appBg,
        color: ui.text,
        minHeight: '100vh',
        p: { xs: 2, md: 3 },
      }}
    >
      {children}
    </Box>
  );
}

export function PlatformMissingResourcePage({
  active,
  title,
  body,
  backLabel,
  backPath,
  breadcrumb,
}: {
  active: PlatformSection;
  title: string;
  body: string;
  backLabel?: string;
  backPath?: string;
  breadcrumb?: BreadcrumbItem[];
}) {
  const navigation = usePlatformNavigation();
  const action =
    backLabel && backPath ? (
      <ToolbarButton
        label={backLabel}
        icon="mdi:arrow-right"
        tone="secondary"
        onClick={() => navigation.push(backPath)}
      />
    ) : undefined;
  return (
    <PlatformRouteFrame active={active}>
      <Stack spacing={2.5}>
        <PlatformContentTitle
          breadcrumb={breadcrumb}
          title={title}
          description={body}
          action={action}
        />
        <EmptyState title={title} body={body} action={action} />
      </Stack>
    </PlatformRouteFrame>
  );
}

export function unresolvedBreadcrumb(name: string, loading: boolean): BreadcrumbItem {
  return loading ? { label: name, loading: true } : { label: name };
}
