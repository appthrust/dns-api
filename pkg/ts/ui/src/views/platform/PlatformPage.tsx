/** @jsxRuntime classic */
import React from 'react';
import { PlatformIntegrationsPage } from './PlatformIntegrationsPage';
import { PLATFORM_INTEGRATIONS_PATH, usePlatformNavigation } from './routes';

export function PlatformPage() {
  const navigation = usePlatformNavigation();
  React.useEffect(() => {
    navigation.replace(PLATFORM_INTEGRATIONS_PATH);
  }, [navigation]);
  return <PlatformIntegrationsPage />;
}
