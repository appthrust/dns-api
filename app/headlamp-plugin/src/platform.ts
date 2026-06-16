import type {
  DnsApiPlatform,
  DnsResourceDescriptor,
  KubeEvent,
  KubeObjectInterface,
  Namespace,
  Provider,
  ProviderIdentity,
  RecordSet,
  Route53Identity,
  Secret,
  Zone,
  ZoneClass,
  ZoneUnit,
} from '@appthrust/dns-api-ui';
import { Router } from '@kinvolk/headlamp-plugin/lib';
import { Notification } from '@kinvolk/headlamp-plugin/lib/components/App/Notifications/notificationsSlice';
import { makeCustomResourceClass } from '@kinvolk/headlamp-plugin/lib/lib/k8s/crd';
import type {
  KubeObjectClass,
  KubeObjectInterface as HeadlampKubeObjectInterface,
} from '@kinvolk/headlamp-plugin/lib/lib/k8s/KubeObject';
import { setNotificationsInStore } from '@kinvolk/headlamp-plugin/lib/lib/notification';
import React from 'react';

export const ZoneResource = makeCustomResourceClass({
  apiInfo: [{ group: 'dns.appthrust.io', version: 'v1alpha1' }],
  kind: 'Zone',
  pluralName: 'zones',
  singularName: 'zone',
  isNamespaced: true,
});

export const ZoneClassResource = makeCustomResourceClass({
  apiInfo: [{ group: 'dns.appthrust.io', version: 'v1alpha1' }],
  kind: 'ZoneClass',
  pluralName: 'zoneclasses',
  singularName: 'zoneclass',
  isNamespaced: true,
});

export const RecordSetResource = makeCustomResourceClass({
  apiInfo: [{ group: 'dns.appthrust.io', version: 'v1alpha1' }],
  kind: 'RecordSet',
  pluralName: 'recordsets',
  singularName: 'recordset',
  isNamespaced: true,
});

export const ZoneUnitResource = makeCustomResourceClass({
  apiInfo: [{ group: 'dns.appthrust.io', version: 'v1alpha1' }],
  kind: 'ZoneUnit',
  pluralName: 'zoneunits',
  singularName: 'zoneunit',
  isNamespaced: true,
});

export const ProviderResource = makeCustomResourceClass({
  apiInfo: [{ group: 'dns.appthrust.io', version: 'v1alpha1' }],
  kind: 'Provider',
  pluralName: 'providers',
  singularName: 'provider',
  isNamespaced: false,
});

export const Route53IdentityResource = makeCustomResourceClass({
  apiInfo: [{ group: 'route53.dns.appthrust.io', version: 'v1alpha1' }],
  kind: 'Route53Identity',
  pluralName: 'route53identities',
  singularName: 'route53identity',
  isNamespaced: true,
});

export const CloudflareIdentityResource = makeCustomResourceClass({
  apiInfo: [{ group: 'cloudflare.dns.appthrust.io', version: 'v1alpha1' }],
  kind: 'CloudflareIdentity',
  pluralName: 'cloudflareidentities',
  singularName: 'cloudflareidentity',
  isNamespaced: true,
});

export const NamespaceResource = makeCustomResourceClass({
  apiInfo: [{ group: '', version: 'v1' }],
  kind: 'Namespace',
  pluralName: 'namespaces',
  singularName: 'namespace',
  isNamespaced: false,
});

export const SecretResource = makeCustomResourceClass({
  apiInfo: [{ group: '', version: 'v1' }],
  kind: 'Secret',
  pluralName: 'secrets',
  singularName: 'secret',
  isNamespaced: true,
});

// Headlamp does not expose the Event deep module to plugins at runtime.
export const EventResource = makeCustomResourceClass({
  apiInfo: [{ group: '', version: 'v1' }],
  kind: 'Event',
  pluralName: 'events',
  singularName: 'event',
  isNamespaced: true,
});

type ResourceMergePatch = {
  metadata?: {
    annotations?: Record<string, string>;
  };
  stringData?: Record<string, string>;
};

type ResourceClass = KubeObjectClass & {
  apiEndpoint: {
    post: (
      body: Partial<HeadlampKubeObjectInterface>,
      queryParams?: Record<string, unknown>,
      cluster?: string
    ) => Promise<unknown>;
    put: (
      body: HeadlampKubeObjectInterface,
      queryParams?: Record<string, unknown>,
      cluster?: string
    ) => Promise<unknown>;
    patch: (
      body: ResourceMergePatch,
      namespace: string,
      name: string,
      queryParams?: Record<string, unknown>,
      cluster?: string
    ) => Promise<unknown>;
  };
  useList: (
    ...args: unknown[]
  ) => [
    Array<{ jsonData: HeadlampKubeObjectInterface; delete: () => Promise<unknown> }> | null,
    unknown
  ];
};

function useResourceList<T extends KubeObjectInterface>(resourceClass: ResourceClass) {
  const [objects, error] = resourceClass.useList() as [
    Array<{ jsonData: T; delete: () => Promise<unknown> }> | null,
    unknown
  ];
  return {
    items: objects?.map(object => object.jsonData) ?? [],
    objects: objects ?? [],
    loading: objects === null && !error,
    error,
  };
}

function useHeadlampDnsData() {
  const zones = useResourceList<Zone>(ZoneResource as ResourceClass);
  const zoneClasses = useResourceList<ZoneClass>(ZoneClassResource as ResourceClass);
  const recordSets = useResourceList<RecordSet>(RecordSetResource as ResourceClass);
  const zoneUnits = useResourceList<ZoneUnit>(ZoneUnitResource as ResourceClass);
  const providers = useResourceList<Provider>(ProviderResource as ResourceClass);
  const route53Identities = useResourceList<Route53Identity>(
    Route53IdentityResource as ResourceClass
  );
  const cloudflareIdentities = useResourceList<ProviderIdentity>(
    CloudflareIdentityResource as ResourceClass
  );
  const identities = {
    items: [...route53Identities.items, ...cloudflareIdentities.items],
    objects: [...route53Identities.objects, ...cloudflareIdentities.objects],
    loading: route53Identities.loading || cloudflareIdentities.loading,
    error: route53Identities.error ?? cloudflareIdentities.error,
  };
  const namespaces = useResourceList<Namespace>(NamespaceResource as ResourceClass);
  const events = useResourceList<KubeEvent>(EventResource as ResourceClass);

  return {
    zones,
    zoneClasses,
    recordSets,
    zoneUnits,
    providers,
    identities,
    namespaces,
    events,
  };
}

const resourceClasses = new Map<string, ResourceClass>([
  ['dns.appthrust.io/v1alpha1/Zone', ZoneResource as ResourceClass],
  ['dns.appthrust.io/v1alpha1/ZoneClass', ZoneClassResource as ResourceClass],
  ['dns.appthrust.io/v1alpha1/RecordSet', RecordSetResource as ResourceClass],
  ['dns.appthrust.io/v1alpha1/ZoneUnit', ZoneUnitResource as ResourceClass],
  ['dns.appthrust.io/v1alpha1/Provider', ProviderResource as ResourceClass],
  ['route53.dns.appthrust.io/v1alpha1/Route53Identity', Route53IdentityResource as ResourceClass],
  [
    'cloudflare.dns.appthrust.io/v1alpha1/CloudflareIdentity',
    CloudflareIdentityResource as ResourceClass,
  ],
  ['v1/Namespace', NamespaceResource as ResourceClass],
  ['v1/Secret', SecretResource as ResourceClass],
  ['v1/Event', EventResource as ResourceClass],
]);

function resourceClassFor(resource: DnsResourceDescriptor): ResourceClass {
  const key = `${resource.apiVersion}/${resource.kind}`;
  const resourceClass = resourceClasses.get(key);
  if (!resourceClass) {
    throw new Error(`unsupported resource: ${key}`);
  }
  return resourceClass;
}

function useHeadlampAccess(
  resource: DnsResourceDescriptor,
  verb: string,
  attrs?: Record<string, string | undefined>
) {
  const [allowed, setAllowed] = React.useState(false);
  const key = JSON.stringify(attrs ?? {});
  const resourceClass = resourceClassFor(resource);

  React.useEffect(() => {
    let cancelled = false;
    resourceClass
      .getAuthorization(verb, attrs)
      .then(result => {
        if (!cancelled) {
          setAllowed(Boolean(result?.status?.allowed));
        }
      })
      .catch(() => {
        if (!cancelled) {
          setAllowed(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [resourceClass, verb, key]);

  return allowed;
}

async function createResource(resourceClass: ResourceClass, manifest: KubeObjectInterface) {
  await resourceClass.apiEndpoint.post(manifest as HeadlampKubeObjectInterface);
}

async function updateResource(resourceClass: ResourceClass, manifest: KubeObjectInterface) {
  await resourceClass.apiEndpoint.put(manifest as HeadlampKubeObjectInterface);
}

async function updateSecretData(namespace: string, name: string, key: string, value: string) {
  await (SecretResource as ResourceClass).apiEndpoint.patch(
    { stringData: { [key]: value } },
    namespace,
    name
  );
}

async function requestZoneReconcile(zone: Zone, value: string) {
  const patch = {
    metadata: {
      annotations: {
        ...(zone.metadata?.annotations ?? {}),
        'dns.appthrust.io/reconcile-request': value,
      },
    },
  };
  await (ZoneResource as ResourceClass).apiEndpoint.patch(
    patch,
    namespaceOf(zone),
    nameOf(zone),
    undefined,
    typeof zone.cluster === 'string' ? zone.cluster : undefined
  );
}

type RouteHistory = {
  location: {
    pathname: string;
  };
  push: (path: string) => void;
  replace: (path: string) => void;
};

function currentHeadlampRoutePath(routeHistory?: RouteHistory): string {
  if (routeHistory?.location.pathname) {
    return routeHistory.location.pathname;
  }

  return window.location.hash.startsWith('#/')
    ? window.location.hash.substring(1)
    : window.location.pathname;
}

function usesHashRouting(): boolean {
  return window.location.hash.startsWith('#/');
}

function withHeadlampRoutePrefix(currentRoutePath: string, dnsPath: string): string {
  const match = currentRoutePath.match(/^(.*?)(\/dns)(?=\/|$)/);
  return match?.[1] ? `${match[1]}${dnsPath}` : dnsPath;
}

function toHeadlampHref(routePath: string): string {
  return usesHashRouting() ? `#${routePath}` : routePath;
}

function navigate(dnsPath: string, mode: 'push' | 'replace', routeHistory?: RouteHistory) {
  const target = withHeadlampRoutePrefix(currentHeadlampRoutePath(routeHistory), dnsPath);

  if (routeHistory) {
    if (mode === 'replace') {
      routeHistory.replace(target);
    } else {
      routeHistory.push(target);
    }
    return;
  }

  if (usesHashRouting()) {
    if (mode === 'replace') {
      window.location.replace(toHeadlampHref(target));
    } else {
      window.location.hash = target;
    }
    return;
  }

  if (mode === 'replace') {
    window.history.replaceState(null, '', target);
  } else {
    window.history.pushState(null, '', target);
  }
  window.dispatchEvent(new PopStateEvent('popstate'));
}

function namespaceOf(resource?: Partial<KubeObjectInterface>) {
  return resource?.metadata?.namespace ?? '';
}

function nameOf(resource?: Partial<KubeObjectInterface>) {
  return resource?.metadata?.name ?? '';
}

function liveYamlPathFor(resource: KubeObjectInterface) {
  const apiVersion = resource.apiVersion ?? '';
  const [group, version] = apiVersion.includes('/')
    ? (apiVersion.split('/') as [string, string])
    : ['-', apiVersion];

  return Router.createRouteURL('dns-api-yaml-editor', {
    group: group || '-',
    version,
    kind: resource.kind,
    namespace: namespaceOf(resource) || '-',
    name: nameOf(resource),
    cluster: typeof resource.cluster === 'string' ? resource.cluster : undefined,
  });
}

function notify(message: string, severity: 'success' | 'warning' | 'error') {
  try {
    setNotificationsInStore(
      new Notification({
        message: `[DNS ${severity}] ${message}`,
        date: new Date(),
      })
    );
  } catch (error) {
    const log =
      severity === 'error' ? console.error : severity === 'warning' ? console.warn : console.info;
    log(`[DNS ${severity}] ${message}`, error);
  }
}

export function createHeadlampPlatform(routeHistory?: RouteHistory): DnsApiPlatform {
  return {
    useDnsData: useHeadlampDnsData,
    useAccess: useHeadlampAccess,
    createZone: manifest => createResource(ZoneResource as ResourceClass, manifest),
    updateZone: manifest => updateResource(ZoneResource as ResourceClass, manifest),
    requestZoneReconcile,
    createRecordSet: manifest => createResource(RecordSetResource as ResourceClass, manifest),
    updateRecordSet: manifest => updateResource(RecordSetResource as ResourceClass, manifest),
    createRoute53Identity: manifest =>
      createResource(Route53IdentityResource as ResourceClass, manifest),
    updateRoute53Identity: manifest =>
      updateResource(Route53IdentityResource as ResourceClass, manifest),
    createCloudflareIdentity: manifest =>
      createResource(CloudflareIdentityResource as ResourceClass, manifest),
    updateCloudflareIdentity: manifest =>
      updateResource(CloudflareIdentityResource as ResourceClass, manifest),
    createSecret: (manifest: Secret) => createResource(SecretResource as ResourceClass, manifest),
    updateSecretData,
    createZoneClass: manifest => createResource(ZoneClassResource as ResourceClass, manifest),
    updateZoneClass: manifest => updateResource(ZoneClassResource as ResourceClass, manifest),
    clipboard: {
      writeText: value => navigator.clipboard.writeText(value),
    },
    navigation: {
      push: dnsPath => navigate(dnsPath, 'push', routeHistory),
      replace: dnsPath => navigate(dnsPath, 'replace', routeHistory),
    },
    externalLinks: {
      open: url => {
        window.open(url, '_blank', 'noopener,noreferrer');
      },
    },
    notifications: {
      success: message => notify(message, 'success'),
      warning: message => notify(message, 'warning'),
      error: message => notify(message, 'error'),
    },
    liveYaml: {
      pathFor: resource => toHeadlampHref(liveYamlPathFor(resource)),
      open: resource => {
        window.location.assign(toHeadlampHref(liveYamlPathFor(resource)));
      },
    },
  };
}
