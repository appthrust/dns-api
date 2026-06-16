import type {
  DnsApiPlatform,
  CloudflareIdentity,
  KubeObjectInterface,
  RecordSet,
  Route53Identity,
  Secret,
  Zone,
  ZoneClass,
} from '@appthrust/dns-api-ui';
import {
  events,
  identities,
  namespaces,
  providers,
  recordSets,
  zoneClasses,
  zoneUnits,
  zones,
} from './mockData';

export type MockScenario =
  | 'data'
  | 'empty'
  | 'rbac-denied'
  | 'admission-error'
  | 'warning-condition'
  | 'provider-pending'
  | 'conflict'
  | 'delete-blocked';

function cloneItems<T>(items: T[]): T[] {
  return JSON.parse(JSON.stringify(items)) as T[];
}

function listState<T extends KubeObjectInterface>(items: T[]) {
  return {
    items,
    objects: items.map(item => ({
      jsonData: item,
      delete: async () => undefined,
    })),
    loading: false,
    error: null,
  };
}

async function logManifest(
  action: string,
  manifest: Zone | ZoneClass | RecordSet | Route53Identity | CloudflareIdentity | Secret
) {
  const safeManifest =
    manifest.kind === 'Secret'
      ? {
          ...manifest,
          stringData: Object.fromEntries(
            Object.keys(manifest.stringData ?? {}).map(key => [key, '<redacted>'])
          ),
        }
      : manifest;
  console.info(`[mockup] ${action}`, safeManifest);
}

async function logZoneReconcileRequest(zone: Zone, value: string) {
  zone.metadata = {
    ...zone.metadata,
    annotations: {
      ...(zone.metadata?.annotations ?? {}),
      'dns.appthrust.io/reconcile-request': value,
    },
  };
  console.info(`[mockup] request Zone reconcile`, zone);
}

async function logSecretDataUpdate(namespace: string, name: string, key: string) {
  console.info(`[mockup] update Secret data`, { namespace, name, stringData: { [key]: '<redacted>' } });
}

function dataForScenario(scenario: MockScenario) {
  const next = {
    zones: cloneItems(zones),
    zoneClasses: cloneItems(zoneClasses),
    recordSets: cloneItems(recordSets),
    zoneUnits: cloneItems(zoneUnits),
    providers: cloneItems(providers),
    identities: cloneItems(identities),
    namespaces: cloneItems(namespaces),
    events: cloneItems(events),
  };

  if (scenario === 'empty') {
    next.zones = [];
    next.recordSets = [];
    next.zoneUnits = [];
    next.events = [];
  }

  if (scenario === 'warning-condition') {
    next.zones[0]!.status = {
      ...next.zones[0]!.status,
      conditions: [
        {
          type: 'Ready',
          status: 'False',
          reason: 'ProviderIdentityNotReady',
          message: 'Route53Identity is not ready yet.',
        },
      ],
    };
    next.events[0]!.type = 'Warning';
    next.events[0]!.reason = 'ProviderIdentityNotReady';
    next.events[0]!.message = 'Route53Identity is not ready yet.';
  }

  if (scenario === 'provider-pending') {
    next.zones[0]!.status = {
      ...next.zones[0]!.status,
      provider: { data: { hostedZoneID: 'Z123MOCK456' } },
      conditions: [{ type: 'Ready', status: 'False', reason: 'ProviderChangePending' }],
    };
  }

  if (scenario === 'conflict') {
    next.recordSets[0]!.status = {
      conditions: [
        {
          type: 'Accepted',
          status: 'False',
          reason: 'RecordSetConflict',
          message: 'The record identity is already owned by another RecordSet.',
        },
      ],
    };
    next.zoneUnits[0]!.spec.recordSets = [
      {
        recordSetNamespace: 'apps',
        recordSetName: 'other-www-a',
        observedGeneration: 1,
        name: 'www',
        type: 'A',
      },
    ];
  }

  if (scenario === 'delete-blocked') {
    next.zoneClasses[0]!.spec.parameters.zoneDeletionPolicy = 'Delete';
  }

  return next;
}

export function createMockPlatform(scenario: MockScenario = 'data'): DnsApiPlatform {
  const mockData = dataForScenario(scenario);
  const shouldRejectWrite = scenario === 'admission-error';
  return {
    useDnsData: () => ({
      zones: listState(mockData.zones),
      zoneClasses: listState(mockData.zoneClasses),
      recordSets: listState(mockData.recordSets),
      zoneUnits: listState(mockData.zoneUnits),
      providers: listState(mockData.providers),
      identities: listState(mockData.identities),
      namespaces: listState(mockData.namespaces),
      events: listState(mockData.events),
    }),
    useAccess: () => scenario !== 'rbac-denied',
    createZone: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the Zone manifest'))
        : logManifest('create Zone', manifest),
    updateZone: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the Zone update'))
        : logManifest('update Zone', manifest),
    requestZoneReconcile: (zone, value) =>
      shouldRejectWrite
        ? Promise.reject(new Error('failed to patch Zone reconcile request'))
        : logZoneReconcileRequest(zone, value),
    createRecordSet: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the RecordSet manifest'))
        : logManifest('create RecordSet', manifest),
    updateRecordSet: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the RecordSet update'))
        : logManifest('update RecordSet', manifest),
    createRoute53Identity: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the Route53Identity manifest'))
        : logManifest('create Route53Identity', manifest),
    updateRoute53Identity: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the Route53Identity update'))
        : logManifest('update Route53Identity', manifest),
    createCloudflareIdentity: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the CloudflareIdentity manifest'))
        : logManifest('create CloudflareIdentity', manifest),
    updateCloudflareIdentity: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the CloudflareIdentity update'))
        : logManifest('update CloudflareIdentity', manifest),
    createSecret: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the Secret manifest'))
        : logManifest('create Secret', manifest),
    updateSecretData: (namespace, name, key) =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the Secret update'))
        : logSecretDataUpdate(namespace, name, key),
    createZoneClass: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the ZoneClass manifest'))
        : logManifest('create ZoneClass', manifest),
    updateZoneClass: manifest =>
      shouldRejectWrite
        ? Promise.reject(new Error('admission webhook denied the ZoneClass update'))
        : logManifest('update ZoneClass', manifest),
    clipboard: {
      writeText: async value => {
        await navigator.clipboard?.writeText(value);
      },
    },
    navigation: {
      push: dnsPath => {
        window.location.hash = dnsPath;
      },
      replace: dnsPath => {
        window.location.replace(`#${dnsPath}`);
      },
    },
    externalLinks: {
      open: url => {
        window.open(url, '_blank', 'noopener,noreferrer');
      },
    },
    notifications: {
      success: message => console.info(`[mockup] ${message}`),
      warning: message => console.warn(`[mockup] ${message}`),
      error: message => console.error(`[mockup] ${message}`),
    },
    liveYaml: {
      pathFor: resource => `#/yaml/${resource.kind}/${resource.metadata?.namespace ?? '-'}/${resource.metadata?.name ?? ''}`,
      open: resource => {
        window.location.hash = `/yaml/${resource.kind}/${resource.metadata?.namespace ?? '-'}/${resource.metadata?.name ?? ''}`;
      },
    },
  };
}
