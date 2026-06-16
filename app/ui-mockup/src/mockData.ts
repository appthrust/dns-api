import type {
  KubeEvent,
  Namespace,
  Provider,
  ProviderIdentity,
  RecordSet,
  Zone,
  ZoneClass,
  ZoneUnit,
} from '@appthrust/dns-api-ui';

const now = '2026-05-29T00:00:00Z';

export const namespaces: Namespace[] = [
  {
    apiVersion: 'v1',
    kind: 'Namespace',
    metadata: { name: 'apps', labels: { 'appthrust.io/dns-access': 'enabled' }, creationTimestamp: now },
  },
  {
    apiVersion: 'v1',
    kind: 'Namespace',
    metadata: { name: 'platform-dns', labels: { 'team': 'platform' }, creationTimestamp: now },
  },
];

export const identities: ProviderIdentity[] = [
  {
    apiVersion: 'route53.dns.appthrust.io/v1alpha1',
    kind: 'Route53Identity',
    metadata: {
      name: 'production-route53',
      namespace: 'platform-dns',
      annotations: { 'dns.appthrust.io/description': 'Production public DNS account.' },
      creationTimestamp: now,
    },
    spec: {
      accountID: '123456789012',
      region: 'ap-northeast-1',
      credentials: { runtime: {} },
      assumeRoleChain: [{ roleARN: 'arn:aws:iam::123456789012:role/dns-api-controller' }],
    },
    status: {
      lastCredentialCheckTime: '2026-05-29T07:45:00Z',
      nextCredentialCheckTime: '2026-05-29T08:45:00Z',
      conditions: [
        { type: 'Accepted', status: 'True', reason: 'Accepted' },
        { type: 'Ready', status: 'True', reason: 'Ready' },
      ],
    },
  },
  {
    apiVersion: 'cloudflare.dns.appthrust.io/v1alpha1',
    kind: 'CloudflareIdentity',
    metadata: {
      name: 'cloudflare-local',
      namespace: 'platform-dns',
      annotations: { 'dns.appthrust.io/description': 'Local Cloudflare account.' },
      creationTimestamp: now,
    },
    spec: {
      accessToken: {
        secretRef: { name: 'cloudflare-api-token', key: 'api-token' },
      },
    },
    status: {
      account: { id: 'bbc95767ba93060b36455d5d6f72d1cb', name: 'local', type: 'standard' },
      accessToken: { id: 'token-id', status: 'active' },
      lastCredentialCheckTime: '2026-05-29T07:45:00Z',
      nextCredentialCheckTime: '2026-05-29T08:45:00Z',
      conditions: [
        { type: 'Accepted', status: 'True', reason: 'Accepted' },
        { type: 'Ready', status: 'True', reason: 'Ready' },
      ],
    },
  },
];

export const zoneClasses: ZoneClass[] = [
  {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'ZoneClass',
    metadata: {
      name: 'public-route53',
      namespace: 'platform-dns',
      annotations: { 'dns.appthrust.io/description': 'Public Route 53 hosted zones.' },
      creationTimestamp: now,
    },
    spec: {
      provider: { name: 'route53.dns.appthrust.io', version: 'v1alpha1' },
      controllerName: 'route53.dns.appthrust.io/public-zone',
      identityRef: { name: 'production-route53' },
      parameters: {
        zoneCreationPolicy: 'Create',
        zoneDeletionPolicy: 'Retain',
        sameNameZonePolicy: 'Deny',
      },
      allowedZones: {
        namespaces: {
          from: 'Selector',
          selector: { matchLabels: { 'appthrust.io/dns-access': 'enabled' } },
        },
      },
    },
    status: { conditions: [{ type: 'Accepted', status: 'True', reason: 'Accepted' }] },
  },
];

export const zones: Zone[] = [
  {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'Zone',
    metadata: {
      name: 'example-com',
      namespace: 'apps',
      annotations: { 'dns.appthrust.io/description': 'Application public zone.' },
      creationTimestamp: now,
      uid: 'mock-zone-uid',
    },
    spec: {
      domainName: 'example.com',
      provider: { name: 'route53.dns.appthrust.io', version: 'v1alpha1' },
      zoneClassRef: { namespace: 'platform-dns', name: 'public-route53' },
      allowedRecordSets: [
        {
          namespaces: { selector: { matchLabels: { 'appthrust.io/dns-access': 'team-a' } } },
          records: [{ name: { pattern: '[a-z0-9]([-.a-z0-9]*[a-z0-9])?\\.team-a' }, types: ['A', 'AAAA', 'TXT', 'CNAME', 'MX', 'CAA', 'NS'] }],
        },
        {
          namespaces: { selector: { matchLabels: { 'appthrust.io/dns-access': 'team-b' } } },
          records: [{ name: { pattern: '[a-z0-9]([-.a-z0-9]*[a-z0-9])?' }, types: ['A'] }],
        },
        {
          namespaces: {
            selector: {
              matchLabels: {
                'appthrust.io/dns-access': 'team-c',
                'appthrust.io/tier': 'frontend',
              },
            },
          },
          records: [
            { name: { pattern: 'app-[a-z0-9]+\\.team-c' }, types: ['A', 'AAAA', 'TXT', 'CNAME', 'MX', 'CAA', 'NS'] },
            { name: { pattern: '@' }, types: ['A'] },
          ],
        },
      ],
    },
    status: {
      nameServers: ['ns-100.awsdns-01.com.', 'ns-200.awsdns-02.net.'],
      provider: { data: { hostedZoneID: 'Z123MOCK456' } },
      conditions: [
        { type: 'Accepted', status: 'True', reason: 'Accepted' },
        { type: 'Programmed', status: 'True', reason: 'Programmed' },
      ],
    },
  },
];

export const recordSets: RecordSet[] = [
  {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'RecordSet',
    metadata: { name: 'www-a', namespace: 'apps', creationTimestamp: now, uid: 'mock-record-uid' },
    spec: {
      zoneRef: { name: 'example-com' },
      provider: { name: 'route53.dns.appthrust.io', version: 'v1alpha1' },
      type: 'A',
      name: 'www',
      ttl: 300,
      a: { addresses: ['192.0.2.10'] },
    },
    status: {
      observedGeneration: 1,
      conditions: [
        { type: 'Accepted', status: 'True', reason: 'Accepted' },
        { type: 'Programmed', status: 'True', reason: 'Programmed' },
      ],
    },
  },
  {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'RecordSet',
    metadata: { name: 'acme-challenge', namespace: 'apps', creationTimestamp: now },
    spec: {
      zoneRef: { name: 'example-com' },
      provider: { name: 'route53.dns.appthrust.io', version: 'v1alpha1' },
      type: 'TXT',
      name: '_acme-challenge',
      ttl: 300,
      txt: { values: ['challenge-token', 'v=spf1 include:_spf.example.net ~all'] },
    },
    status: {
      observedGeneration: 1,
      conditions: [
        { type: 'Accepted', status: 'True', reason: 'Accepted' },
        { type: 'Programmed', status: 'True', reason: 'Programmed' },
      ],
    },
  },
];

export const zoneUnits: ZoneUnit[] = [
  {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'ZoneUnit',
    metadata: { name: 'example-com', namespace: 'apps', creationTimestamp: now },
    spec: {
      provider: { name: 'route53.dns.appthrust.io', version: 'v1alpha1' },
      zone: { ref: { namespace: 'apps', name: 'example-com' }, domainName: 'example.com' },
      recordSets: [
        {
          recordSetNamespace: 'apps',
          recordSetName: 'www-a',
          observedGeneration: 1,
          name: 'www',
          type: 'A',
        },
        {
          recordSetNamespace: 'apps',
          recordSetName: 'acme-challenge',
          observedGeneration: 1,
          name: '_acme-challenge',
          type: 'TXT',
        },
      ],
    },
    status: {
      conditions: [{ type: 'Programmed', status: 'True', reason: 'Programmed' }],
    },
  },
];

export const providers: Provider[] = [
  {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'Provider',
    metadata: { name: 'route53.dns.appthrust.io', creationTimestamp: now },
    spec: {
      display: {
        name: 'Amazon Route 53',
        description: 'Public DNS zones and record sets managed through Route 53.',
        logo: {
          url: 'https://cdn.jsdelivr.net/gh/glincker/thesvg@main/public/icons/aws-amazon-route-53/default.svg',
        },
      },
      versions: [
        {
          name: 'v1alpha1',
          served: true,
          storage: true,
          identity: {
            resource: {
              group: 'route53.dns.appthrust.io',
              kind: 'Route53Identity',
              scope: 'Namespaced',
            },
          },
          recordSet: { supportedTypes: ['A', 'AAAA', 'TXT', 'CNAME', 'MX', 'CAA', 'NS'] },
        },
      ],
    },
  },
  {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'Provider',
    metadata: { name: 'cloudflare.dns.appthrust.io', creationTimestamp: now },
    spec: {
      display: {
        name: 'Cloudflare DNS',
        description: 'Public DNS zones and record sets managed through Cloudflare DNS.',
        logo: { url: 'https://cdn.simpleicons.org/cloudflare' },
      },
      versions: [
        {
          name: 'v1alpha1',
          served: true,
          storage: true,
          identity: {
            resource: {
              group: 'cloudflare.dns.appthrust.io',
              kind: 'CloudflareIdentity',
              scope: 'Namespaced',
            },
          },
          recordSet: { supportedTypes: ['A', 'AAAA', 'TXT', 'CNAME', 'MX', 'CAA', 'NS'] },
        },
      ],
    },
  },
];

export const events: KubeEvent[] = [
  {
    apiVersion: 'v1',
    kind: 'Event',
    metadata: { name: 'example-com-ready', namespace: 'apps', creationTimestamp: now },
    involvedObject: {
      apiVersion: 'dns.appthrust.io/v1alpha1',
      kind: 'Zone',
      namespace: 'apps',
      name: 'example-com',
      uid: 'mock-zone-uid',
    },
    type: 'Normal',
    reason: 'Programmed',
    message: 'Hosted zone is programmed.',
    count: 1,
    lastTimestamp: now,
  },
];
