import { canonicalRecordName } from './dns';

export { canonicalRecordName };

export interface KubeObjectInterface {
  apiVersion?: string;
  kind?: string;
  cluster?: string;
  metadata?: {
    name?: string;
    namespace?: string;
    labels?: Record<string, string>;
    annotations?: Record<string, string>;
    creationTimestamp?: string;
    generation?: number;
    resourceVersion?: string;
    uid?: string;
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export type ConditionStatus = 'True' | 'False' | 'Unknown';

export interface DnsCondition {
  type: string;
  status: ConditionStatus;
  reason?: string;
  message?: string;
  observedGeneration?: number;
  lastTransitionTime?: string;
}

export interface ObjectRef {
  namespace?: string;
  name: string;
}

export interface ProviderRef {
  name: string;
  version: string;
}

export interface Zone extends KubeObjectInterface {
  apiVersion: 'dns.appthrust.io/v1alpha1';
  kind: 'Zone';
  spec: {
    domainName: string;
    zoneClassRef: ObjectRef;
    provider: ProviderRef;
    adoption?: Record<string, unknown>;
    allowedRecordSets?: AllowedRecordSet[];
  };
  status?: {
    nameServers?: string[];
    provider?: {
      data?: {
        hostedZoneID?: string;
        zone?: { id?: string };
      } & Record<string, unknown>;
    };
    conditions?: DnsCondition[];
  };
}

export interface ZoneClass extends KubeObjectInterface {
  apiVersion: 'dns.appthrust.io/v1alpha1';
  kind: 'ZoneClass';
  spec: {
    provider: ProviderRef;
    controllerName?: string;
    identityRef?: {
      name?: string;
    };
    parameters: {
      identityRef?: {
        name?: string;
      };
      zoneCreationPolicy?: 'Create' | 'Deny';
      zoneDeletionPolicy?: 'Delete' | 'Retain';
      sameNameZonePolicy?: 'Allow' | 'Deny';
      tags?: Record<string, string>;
      [key: string]: unknown;
    };
    allowedZones: {
      namespaces: NamespacePolicy;
    };
  };
  status?: {
    conditions?: DnsCondition[];
  };
}

export interface RecordSet extends KubeObjectInterface {
  apiVersion: 'dns.appthrust.io/v1alpha1';
  kind: 'RecordSet';
  spec: {
    zoneRef: ObjectRef;
    provider: ProviderRef;
    type: string;
    name: string;
    ttl?: number;
    a?: {
      addresses?: string[];
    };
    aaaa?: {
      addresses?: string[];
    };
    txt?: {
      values?: string[];
    };
    cname?: {
      target?: string;
    };
    mx?: {
      records?: Array<{
        preference?: number;
        exchange?: string;
      }>;
    };
    caa?: {
      records?: Array<{
        flags?: number;
        tag?: string;
        value?: string;
      }>;
    };
    ns?: {
      nameServers?: string[];
    };
    options?: Record<string, unknown>;
    adoption?: Record<string, unknown>;
  };
  status?: {
    observedGeneration?: number;
    zone?: {
      ref: {
        namespace: string;
        name: string;
      };
    };
    provider?: {
      data?: Record<string, unknown>;
    };
    conditions?: DnsCondition[];
  };
}

export interface ZoneUnit extends KubeObjectInterface {
  apiVersion: 'dns.appthrust.io/v1alpha1';
  kind: 'ZoneUnit';
  spec: {
    provider?: ProviderRef;
    zone?: {
      ref?: {
        namespace: string;
        name: string;
      };
      domainName?: string;
    };
    recordSets?: ZoneUnitRecordSetSpec[];
  };
  status?: {
    conditions?: DnsCondition[];
    recordSets?: ZoneUnitRecordSetStatus[];
  };
}

export interface ZoneUnitRecordSetSpec {
  recordSetNamespace: string;
  recordSetName: string;
  observedGeneration?: number;
  name: string;
  type: string;
  deletionRequested?: boolean;
}

export interface ZoneUnitRecordSetStatus {
  recordSetNamespace: string;
  recordSetName: string;
  observedGeneration?: number;
  deletionCompleted?: boolean;
  provider?: {
    data?: Record<string, unknown>;
  };
  conditions?: DnsCondition[];
}

export interface Provider extends KubeObjectInterface {
  apiVersion: 'dns.appthrust.io/v1alpha1';
  kind: 'Provider';
  spec: {
    display: {
      name: string;
      description?: string;
      logo?: {
        url?: string;
      };
    };
    versions: Array<{
      name: string;
      served: boolean;
      storage?: boolean;
      deprecated?: boolean;
      identity?: {
        resource?: {
          group?: string;
          kind?: string;
          scope?: 'Namespaced';
        };
      };
      zoneClass?: {
        schemas?: ProviderZoneClassSchemas;
        validationRules?: ProviderValidationRule[];
      };
      zone?: {
        schemas?: ProviderZoneSchemas;
        validationRules?: ProviderValidationRule[];
      };
      recordSet?: {
        supportedTypes?: string[];
        schemas?: ProviderRecordSetSchemas;
        validationRules?: ProviderValidationRule[];
        disableValidations?: Array<{
          name: string;
          when: string;
        }>;
      };
    }>;
  };
}

export interface Route53Identity extends KubeObjectInterface {
  apiVersion: 'route53.dns.appthrust.io/v1alpha1';
  kind: 'Route53Identity';
  spec: {
    accountID: string;
    region: string;
    credentials: {
      runtime?: Record<string, never>;
    };
    assumeRoleChain?: Array<{
      roleARN: string;
      externalID?: string;
      sessionName?: string;
    }>;
  };
  status?: {
    lastCredentialCheckTime?: string;
    nextCredentialCheckTime?: string;
    conditions?: DnsCondition[];
  };
}

export interface CloudflareIdentity extends KubeObjectInterface {
  apiVersion: 'cloudflare.dns.appthrust.io/v1alpha1';
  kind: 'CloudflareIdentity';
  spec: {
    accessToken: {
      secretRef: {
        name: string;
        key: string;
      };
    };
  };
  status?: {
    account?: {
      id?: string;
      name?: string;
      type?: string;
    };
    accessToken?: {
      id?: string;
      status?: string;
      expiresOn?: string;
      notBefore?: string;
    };
    lastCredentialCheckTime?: string;
    nextCredentialCheckTime?: string;
    conditions?: DnsCondition[];
  };
}

export type ProviderIdentity = Route53Identity | CloudflareIdentity;

export interface KubeEvent extends KubeObjectInterface {
  involvedObject?: {
    apiVersion?: string;
    kind?: string;
    namespace?: string;
    name?: string;
    uid?: string;
  };
  reason?: string;
  message?: string;
  type?: string;
  count?: number;
  series?: {
    count?: number;
    lastObservedTime?: string;
  };
  lastTimestamp?: string;
  eventTime?: string;
}

export interface Namespace extends KubeObjectInterface {
  apiVersion: 'v1';
  kind: 'Namespace';
  metadata: KubeObjectInterface['metadata'] & {
    labels?: Record<string, string>;
  };
}

export interface Secret extends KubeObjectInterface {
  apiVersion: 'v1';
  kind: 'Secret';
  type?: string;
  stringData?: Record<string, string>;
  data?: Record<string, string>;
}

export interface NamespacePolicy {
  from?: 'Same' | 'Selector' | 'All';
  selector?: {
    matchLabels?: Record<string, string>;
    matchExpressions?: Array<Record<string, unknown>>;
  };
}

export interface AllowedRecordSet {
  namespaces: {
    selector: {
      matchLabels?: Record<string, string>;
      matchExpressions?: Array<Record<string, unknown>>;
    };
  };
  records: Array<{
    name: {
      pattern: string;
    };
    types: string[];
  }>;
}

export interface ProviderZoneClassSchemas {
  parameters?: OpenAPISchemaHolder;
}

export interface ProviderZoneSchemas {
  adoption?: OpenAPISchemaHolder;
  statusProviderData?: OpenAPISchemaHolder;
}

export interface ProviderRecordSetSchemas {
  options?: OpenAPISchemaHolder;
  adoption?: OpenAPISchemaHolder;
  statusProviderData?: OpenAPISchemaHolder;
}

export interface OpenAPISchemaHolder {
  openAPIV3Schema?: Record<string, unknown>;
}

export interface ProviderValidationRule {
  rule: string;
  message?: string;
}

export interface Route53Change {
  id?: string;
  status?: 'PENDING' | 'INSYNC';
  submittedAt?: string;
}

export interface Route53PendingChange extends Route53Change {
  operation?: 'CREATE' | 'DELETE';
}

export interface Route53PendingRecordSetChange extends Route53Change {
  operation?: 'UPSERT_BATCH' | 'DELETE_BATCH';
  affectedRecordSets?: Array<{
    namespace: string;
    name: string;
  }>;
}

export type DnsResourceDescriptor = {
  apiVersion: string;
  kind: string;
  pluralName: string;
  singularName: string;
  isNamespaced: boolean;
};

export const ZoneResource = {
  apiVersion: 'dns.appthrust.io/v1alpha1',
  kind: 'Zone',
  pluralName: 'zones',
  singularName: 'zone',
  isNamespaced: true,
} as const satisfies DnsResourceDescriptor;

export const ZoneClassResource = {
  apiVersion: 'dns.appthrust.io/v1alpha1',
  kind: 'ZoneClass',
  pluralName: 'zoneclasses',
  singularName: 'zoneclass',
  isNamespaced: true,
} as const satisfies DnsResourceDescriptor;

export const RecordSetResource = {
  apiVersion: 'dns.appthrust.io/v1alpha1',
  kind: 'RecordSet',
  pluralName: 'recordsets',
  singularName: 'recordset',
  isNamespaced: true,
} as const satisfies DnsResourceDescriptor;

export const ZoneUnitResource = {
  apiVersion: 'dns.appthrust.io/v1alpha1',
  kind: 'ZoneUnit',
  pluralName: 'zoneunits',
  singularName: 'zoneunit',
  isNamespaced: true,
} as const satisfies DnsResourceDescriptor;

export const ProviderResource = {
  apiVersion: 'dns.appthrust.io/v1alpha1',
  kind: 'Provider',
  pluralName: 'providers',
  singularName: 'provider',
  isNamespaced: false,
} as const satisfies DnsResourceDescriptor;

export const Route53IdentityResource = {
  apiVersion: 'route53.dns.appthrust.io/v1alpha1',
  kind: 'Route53Identity',
  pluralName: 'route53identities',
  singularName: 'route53identity',
  isNamespaced: true,
} as const satisfies DnsResourceDescriptor;

export const CloudflareIdentityResource = {
  apiVersion: 'cloudflare.dns.appthrust.io/v1alpha1',
  kind: 'CloudflareIdentity',
  pluralName: 'cloudflareidentities',
  singularName: 'cloudflareidentity',
  isNamespaced: true,
} as const satisfies DnsResourceDescriptor;

export const NamespaceResource = {
  apiVersion: 'v1',
  kind: 'Namespace',
  pluralName: 'namespaces',
  singularName: 'namespace',
  isNamespaced: false,
} as const satisfies DnsResourceDescriptor;

export const SecretResource = {
  apiVersion: 'v1',
  kind: 'Secret',
  pluralName: 'secrets',
  singularName: 'secret',
  isNamespaced: true,
} as const satisfies DnsResourceDescriptor;

export const EventResource = {
  apiVersion: 'v1',
  kind: 'Event',
  pluralName: 'events',
  singularName: 'event',
  isNamespaced: true,
} as const satisfies DnsResourceDescriptor;

export function conditionOf(resource: { status?: { conditions?: DnsCondition[] } }, type: string) {
  return resource.status?.conditions?.find(condition => condition.type === type);
}

export function isBadCondition(condition?: DnsCondition) {
  if (!condition) {
    return false;
  }
  return condition.status !== 'True';
}

export function namespaceOf(resource?: Partial<KubeObjectInterface>) {
  return resource?.metadata?.namespace ?? '';
}

export function nameOf(resource?: Partial<KubeObjectInterface>) {
  return resource?.metadata?.name ?? '';
}

export function resourceKey(resource: KubeObjectInterface) {
  const namespace = namespaceOf(resource);
  return namespace ? `${namespace}/${nameOf(resource)}` : nameOf(resource);
}

export function zoneRefNamespace(recordSet: RecordSet) {
  return recordSet.spec.zoneRef.namespace || namespaceOf(recordSet);
}

export function zoneClassRefNamespace(zone: Zone) {
  return zone.spec.zoneClassRef.namespace || namespaceOf(zone);
}

export function zoneClassIdentityName(zoneClass?: ZoneClass) {
  return zoneClass?.spec.identityRef?.name ?? zoneClass?.spec.parameters.identityRef?.name ?? '';
}

export function fqdnForRecordSet(recordSet: RecordSet, zones: Zone[]) {
  const zone = zones.find(
    item =>
      namespaceOf(item) === zoneRefNamespace(recordSet) &&
      nameOf(item) === recordSet.spec.zoneRef.name
  );
  if (!zone) {
    return recordSet.spec.name;
  }
  return canonicalRecordName(recordSet.spec.name, zone.spec.domainName);
}
