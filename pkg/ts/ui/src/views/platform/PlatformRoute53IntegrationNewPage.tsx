/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { IdentityFormPage } from './IntegrationForm';
import { platformNamespaces } from './resources';
import {
  INTEGRATIONS_BREADCRUMB,
  PLATFORM_INTEGRATION_NEW_PATH,
  PLATFORM_INTEGRATIONS_PATH,
  PlatformRouteFrame,
  usePlatformNavigation,
} from './routes';

export function PlatformRoute53IntegrationNewPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  return (
    <PlatformRouteFrame active="integrations">
      <IdentityFormPage
        breadcrumb={[INTEGRATIONS_BREADCRUMB]}
        namespaces={platformNamespaces(data)}
        onBack={() => navigation.push(PLATFORM_INTEGRATION_NEW_PATH)}
        onSaved={() => navigation.push(PLATFORM_INTEGRATIONS_PATH)}
      />
    </PlatformRouteFrame>
  );
}
