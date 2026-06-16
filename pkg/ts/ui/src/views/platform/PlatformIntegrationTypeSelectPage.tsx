/** @jsxRuntime classic */
import React from 'react';
import { IntegrationTypeSelectPage } from './IntegrationForm';
import {
  INTEGRATIONS_BREADCRUMB,
  PLATFORM_CLOUDFLARE_INTEGRATION_NEW_PATH,
  PLATFORM_INTEGRATIONS_PATH,
  PLATFORM_ROUTE53_INTEGRATION_NEW_PATH,
  PlatformRouteFrame,
  usePlatformNavigation,
} from './routes';

export function PlatformIntegrationTypeSelectPage() {
  const navigation = usePlatformNavigation();
  return (
    <PlatformRouteFrame active="integrations">
      <IntegrationTypeSelectPage
        breadcrumb={[INTEGRATIONS_BREADCRUMB]}
        onCancel={() => navigation.push(PLATFORM_INTEGRATIONS_PATH)}
        onSelectCloudflare={() => navigation.push(PLATFORM_CLOUDFLARE_INTEGRATION_NEW_PATH)}
        onSelectRoute53={() => navigation.push(PLATFORM_ROUTE53_INTEGRATION_NEW_PATH)}
      />
    </PlatformRouteFrame>
  );
}
