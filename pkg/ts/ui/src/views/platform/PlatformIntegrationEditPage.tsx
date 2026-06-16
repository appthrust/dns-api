/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { CloudflareIdentityFormPage, IdentityFormPage } from './IntegrationForm';
import { findIdentity, platformNamespaces } from './resources';
import {
  INTEGRATIONS_BREADCRUMB,
  integrationBreadcrumb,
  integrationDetailPath,
  PLATFORM_INTEGRATIONS_PATH,
  PlatformMissingResourcePage,
  PlatformRouteFrame,
  unresolvedBreadcrumb,
  usePlatformNavigation,
  useResourceRouteParams,
} from './routes';

export function PlatformIntegrationEditPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const params = useResourceRouteParams();
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

  return (
    <PlatformRouteFrame active="integrations">
      {identity.kind === 'CloudflareIdentity' ? (
        <CloudflareIdentityFormPage
          breadcrumb={[INTEGRATIONS_BREADCRUMB, integrationBreadcrumb(identity)]}
          namespaces={platformNamespaces(data)}
          identity={identity}
          onBack={() => navigation.push(integrationDetailPath(identity))}
          onSaved={() => navigation.push(PLATFORM_INTEGRATIONS_PATH)}
        />
      ) : (
        <IdentityFormPage
          breadcrumb={[INTEGRATIONS_BREADCRUMB, integrationBreadcrumb(identity)]}
          namespaces={platformNamespaces(data)}
          identity={identity}
          onBack={() => navigation.push(integrationDetailPath(identity))}
          onSaved={() => navigation.push(PLATFORM_INTEGRATIONS_PATH)}
        />
      )}
    </PlatformRouteFrame>
  );
}
