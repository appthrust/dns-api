/** @jsxRuntime classic */
import { Stack } from '../components/primitives';
import React from 'react';
import { DnsTextField } from './common/ui';

export function SettingsPanel({
  data,
  onDataChange,
}: {
  data?: {
    defaultNamespace?: string;
    warningReasons?: string;
  };
  onDataChange: (data: Record<string, unknown>) => void;
}) {
  return (
    <Stack spacing={2}>
      <DnsTextField
        label="Default namespace"
        value={data?.defaultNamespace ?? ''}
        onChange={event => onDataChange({ ...data, defaultNamespace: event.target.value })}
      />
      <DnsTextField
        label="Warning reasons"
        value={
          data?.warningReasons ?? 'RecordSetConflict,ProviderConflict,ProviderIdentityNotReady'
        }
        onChange={event => onDataChange({ ...data, warningReasons: event.target.value })}
      />
    </Stack>
  );
}
