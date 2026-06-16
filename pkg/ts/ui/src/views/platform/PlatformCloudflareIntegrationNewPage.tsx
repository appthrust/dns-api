/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { CloudflareIdentityFormPage } from './IntegrationForm';
import { platformNamespaces } from './resources';
import {
  INTEGRATIONS_BREADCRUMB,
  PLATFORM_INTEGRATION_NEW_PATH,
  PLATFORM_INTEGRATIONS_PATH,
  PlatformRouteFrame,
  usePlatformNavigation,
} from './routes';

export function PlatformCloudflareIntegrationNewPage() {
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  return (
    <PlatformRouteFrame active="integrations">
      <CloudflareIdentityFormPage
        breadcrumb={[INTEGRATIONS_BREADCRUMB]}
        namespaces={platformNamespaces(data)}
        onBack={() => navigation.push(PLATFORM_INTEGRATION_NEW_PATH)}
        onSaved={() => navigation.push(PLATFORM_INTEGRATIONS_PATH)}
      />
    </PlatformRouteFrame>
  );
}
