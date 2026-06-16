/** @jsxRuntime classic */
import React from 'react';
import { useAccess } from '../../api/dns';
import {
  CloudflareIdentityResource,
  nameOf,
  namespaceOf,
  Route53IdentityResource,
  zoneClassIdentityName,
  zoneClassRefNamespace,
  ZoneClassResource,
} from '../../resources';
import type { ProviderIdentity, Route53Identity, Zone, ZoneClass } from '../../types/resources';
import { DnsData, ProviderBadge, providerForResource, unique } from '../common/ui';

export function zonesReferencingZoneClass(zones: Zone[], zoneClass: ZoneClass) {
  return zones.filter(
    zone =>
      zoneClassRefNamespace(zone) === namespaceOf(zoneClass) &&
      zone.spec.zoneClassRef.name === nameOf(zoneClass)
  );
}

export function zoneClassesReferencingIdentity(
  zoneClasses: ZoneClass[],
  identity: ProviderIdentity
) {
  return zoneClasses.filter(
    zoneClass =>
      namespaceOf(zoneClass) === namespaceOf(identity) &&
      zoneClassIdentityName(zoneClass) === nameOf(identity)
  );
}

export function integrationFields(
  identity: ProviderIdentity,
  usedBy: number
): Array<[string, React.ReactNode]> {
  const provider = providerForResource(identity);
  const baseFields: Array<[string, React.ReactNode]> = [
    ['Provider', <ProviderBadge key="provider" provider={provider} />],
    ['Kind', identity.kind],
    ['Namespace', namespaceOf(identity)],
    ['Name', nameOf(identity)],
    [
      identity.kind === 'CloudflareIdentity' ? 'Cloudflare account ID' : 'AWS account ID',
      identity.kind === 'CloudflareIdentity'
        ? identity.status?.account?.id ?? '-'
        : identity.spec.accountID,
    ],
  ];
  if (identity.kind === 'Route53Identity') {
    baseFields.push(['AWS region', identity.spec.region]);
  }
  if (identity.kind === 'CloudflareIdentity') {
    baseFields.push(
      ['Token Secret', identity.spec.accessToken.secretRef.name],
      ['Token key', identity.spec.accessToken.secretRef.key],
      ['Token status', identity.status?.accessToken?.status ?? '-']
    );
  }
  return [
    ...baseFields,
    ['Last credential check', identity.status?.lastCredentialCheckTime || 'Not checked'],
    ['Next credential check', identity.status?.nextCredentialCheckTime || 'Not scheduled'],
    ['Used by', `${usedBy} ZoneClass${usedBy === 1 ? '' : 'es'}`],
    ['Created', identity.metadata?.creationTimestamp ?? '-'],
  ];
}

export function zoneClassPolicyFields(zoneClass: ZoneClass): Array<[string, React.ReactNode]> {
  const creationPolicy = zoneClass.spec.parameters.zoneCreationPolicy ?? 'Create';
  const isRoute53 = providerForResource(zoneClass) === 'AWS Route 53';
  return [
    ['Zone creation policy', creationPolicy],
    ...(isRoute53 && creationPolicy === 'Create'
      ? ([
          ['Duplicate hosted zone policy', zoneClass.spec.parameters.sameNameZonePolicy ?? 'Deny'],
        ] as Array<[string, React.ReactNode]>)
      : []),
    ['Zone deletion policy', zoneClass.spec.parameters.zoneDeletionPolicy ?? 'Retain'],
  ];
}

export function useIdentityAccess() {
  const canCreateRoute53Identity = useAccess(Route53IdentityResource, 'create', {
    group: 'route53.dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'route53identities',
  });
  const canCreateCloudflareIdentity = useAccess(CloudflareIdentityResource, 'create', {
    group: 'cloudflare.dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'cloudflareidentities',
  });
  const canDeleteRoute53Identity = useAccess(Route53IdentityResource, 'delete', {
    group: 'route53.dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'route53identities',
  });
  const canDeleteCloudflareIdentity = useAccess(CloudflareIdentityResource, 'delete', {
    group: 'cloudflare.dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'cloudflareidentities',
  });
  const canUpdateCloudflareIdentity = useAccess(CloudflareIdentityResource, 'update', {
    group: 'cloudflare.dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'cloudflareidentities',
  });
  const canUpdateRoute53Identity = useAccess(Route53IdentityResource, 'update', {
    group: 'route53.dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'route53identities',
  });
  return {
    canCreateIdentity: canCreateRoute53Identity || canCreateCloudflareIdentity,
    canUpdateCloudflareIdentity,
    canUpdateRoute53Identity,
    canDeleteCloudflareIdentity,
    canDeleteRoute53Identity,
    canUpdateIdentity: canUpdateRoute53Identity || canUpdateCloudflareIdentity,
    canDeleteIdentity: canDeleteRoute53Identity || canDeleteCloudflareIdentity,
  };
}

export function useZoneClassAccess() {
  return {
    canCreateZoneClass: useAccess(ZoneClassResource, 'create', {
      group: 'dns.appthrust.io',
      version: 'v1alpha1',
      resource: 'zoneclasses',
    }),
    canUpdateZoneClass: useAccess(ZoneClassResource, 'update', {
      group: 'dns.appthrust.io',
      version: 'v1alpha1',
      resource: 'zoneclasses',
    }),
    canDeleteZoneClass: useAccess(ZoneClassResource, 'delete', {
      group: 'dns.appthrust.io',
      version: 'v1alpha1',
      resource: 'zoneclasses',
    }),
  };
}

export function platformNamespaces(data: DnsData): string[] {
  return unique([
    ...data.namespaces.items.map(nameOf),
    ...data.zoneClasses.items.map(namespaceOf),
    ...data.identities.items.map(namespaceOf),
    ...data.zones.items.map(namespaceOf),
    'default',
  ]);
}

export function findIdentity(
  identities: ProviderIdentity[],
  namespace: string,
  name: string
): ProviderIdentity | undefined {
  return identities.find(
    identity => namespaceOf(identity) === namespace && nameOf(identity) === name
  );
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
