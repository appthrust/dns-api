/** @jsxRuntime classic */
import React from 'react';
import { DnsPlatformProvider, type DnsApiPlatform } from '../platform';
import { DnsThemeProvider, type DnsTheme } from '../theme';
import { OverviewPage } from '../views/OverviewPage';

export function DnsApiApp({
  platform,
  theme,
  children,
}: {
  platform: DnsApiPlatform;
  theme?: DnsTheme;
  children?: React.ReactNode;
}) {
  return (
    <DnsThemeProvider theme={theme}>
      <DnsPlatformProvider platform={platform}>{children ?? <OverviewPage />}</DnsPlatformProvider>
    </DnsThemeProvider>
  );
}
