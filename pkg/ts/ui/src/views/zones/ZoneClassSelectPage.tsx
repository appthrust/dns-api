/** @jsxRuntime classic */
import React from 'react';
import { useDnsData } from '../../api/dns';
import { newZonePath, useZonesNavigation, ZONES_BREADCRUMB, ZONES_PATH } from './routes';
import { ZoneClassPickerForZonePage } from './ZoneClassPicker';

export function ZoneClassSelectPage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  return (
    <ZoneClassPickerForZonePage
      breadcrumb={[ZONES_BREADCRUMB]}
      zoneClasses={data.zoneClasses.items}
      onCancel={() => navigation.push(ZONES_PATH)}
      onSelect={zoneClass => navigation.push(newZonePath(zoneClass))}
    />
  );
}
