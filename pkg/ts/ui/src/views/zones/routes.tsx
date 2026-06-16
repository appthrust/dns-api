/** @jsxRuntime classic */
import React from 'react';
import { useParams } from 'react-router-dom';
import { useDnsPlatform } from '../../platform';
import { nameOf, namespaceOf } from '../../resources';
import type { RecordSet, Zone, ZoneClass } from '../../types/resources';
import type { BreadcrumbItem } from '../common/ui';

export const ZONES_PATH = '/dns/zones';
export const ZONE_CLASS_SELECT_PATH = '/dns/zones/new';

export type ZoneRouteParams = {
  namespace?: string;
  name?: string;
};

export type ZoneClassRouteParams = {
  zoneClassNamespace?: string;
  zoneClassName?: string;
};

export type RecordSetRouteParams = ZoneRouteParams & {
  recordNamespace?: string;
  recordName?: string;
};

export function pathPart(value: string): string {
  return encodeURIComponent(value);
}

export function routeParam(value?: string): string {
  return value ? decodeURIComponent(value) : '';
}

export function useZonesNavigation() {
  const platform = useDnsPlatform();
  return React.useMemo(
    () => ({
      push: platform.navigation.push,
      replace: platform.navigation.replace,
    }),
    [platform]
  );
}

export function zonePath(zone: Zone): string {
  return `${ZONES_PATH}/${pathPart(namespaceOf(zone))}/${pathPart(nameOf(zone))}`;
}

export function zoneEditPath(zone: Zone): string {
  return `${zonePath(zone)}/edit`;
}

export function zoneDeletePath(zone: Zone): string {
  return `${zonePath(zone)}/delete`;
}

export function newZonePath(zoneClass: ZoneClass): string {
  return `${ZONE_CLASS_SELECT_PATH}/${pathPart(namespaceOf(zoneClass))}/${pathPart(
    nameOf(zoneClass)
  )}`;
}

export function recordSetNewPath(zone: Zone): string {
  return `${zonePath(zone)}/recordsets/new`;
}

export function recordSetPath(zone: Zone, recordSet: RecordSet): string {
  return `${zonePath(zone)}/recordsets/${pathPart(namespaceOf(recordSet))}/${pathPart(
    nameOf(recordSet)
  )}`;
}

export function recordSetEditPath(zone: Zone, recordSet: RecordSet): string {
  return `${recordSetPath(zone, recordSet)}/edit`;
}

export function recordSetDeletePath(zone: Zone, recordSet: RecordSet): string {
  return `${recordSetPath(zone, recordSet)}/delete`;
}

export const ZONES_BREADCRUMB: BreadcrumbItem = { label: 'Zones', path: ZONES_PATH };

export function zoneBreadcrumb(zone: Zone): BreadcrumbItem {
  return { label: nameOf(zone), path: zonePath(zone) };
}

export function recordSetBreadcrumb(zone: Zone, recordSet: RecordSet): BreadcrumbItem {
  return {
    label: `${nameOf(recordSet)} (${recordSet.spec.type})`,
    path: recordSetPath(zone, recordSet),
  };
}

export function unresolvedBreadcrumb(name: string, loading: boolean): BreadcrumbItem {
  return loading ? { label: name, loading: true } : { label: name };
}

export function useZoneRouteParams(): { namespace: string; name: string } {
  const params = useParams<ZoneRouteParams>();
  return {
    namespace: routeParam(params.namespace),
    name: routeParam(params.name),
  };
}

export function useZoneClassRouteParams(): { namespace: string; name: string } {
  const params = useParams<ZoneClassRouteParams>();
  return {
    namespace: routeParam(params.zoneClassNamespace),
    name: routeParam(params.zoneClassName),
  };
}

export function useRecordSetRouteParams(): {
  namespace: string;
  name: string;
  recordNamespace: string;
  recordName: string;
} {
  const params = useParams<RecordSetRouteParams>();
  return {
    namespace: routeParam(params.namespace),
    name: routeParam(params.name),
    recordNamespace: routeParam(params.recordNamespace),
    recordName: routeParam(params.recordName),
  };
}

export function findZone(zones: Zone[], namespace: string, name: string): Zone | undefined {
  return zones.find(zone => namespaceOf(zone) === namespace && nameOf(zone) === name);
}

export function findZoneClass(
  zoneClasses: ZoneClass[],
  namespace: string,
  name: string
): ZoneClass | undefined {
  return zoneClasses.find(
    zoneClass => namespaceOf(zoneClass) === namespace && nameOf(zoneClass) === name
  );
}

export function findRecordSet(
  recordSets: RecordSet[],
  namespace: string,
  name: string
): RecordSet | undefined {
  return recordSets.find(
    recordSet => namespaceOf(recordSet) === namespace && nameOf(recordSet) === name
  );
}
