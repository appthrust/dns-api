/** @jsxRuntime classic */
import { DnsApiApp } from '@appthrust/dns-api-ui';
import React from 'react';
import { useHistory } from 'react-router-dom';
import { createHeadlampPlatform } from './platform';
import { useHeadlampTheme } from './theme';

export function HeadlampPluginRoot({ children }: { children: React.ReactNode }) {
  const history = useHistory();
  const theme = useHeadlampTheme();
  const platform = React.useMemo(() => createHeadlampPlatform(history), [history]);

  return (
    <DnsApiApp platform={platform} theme={theme}>
      {children}
    </DnsApiApp>
  );
}
