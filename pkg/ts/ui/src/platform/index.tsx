/** @jsxRuntime classic */
import React from 'react';
import type {
  DnsResourceDescriptor,
  KubeEvent,
  KubeObjectInterface,
  Namespace,
  Provider,
  ProviderIdentity,
  RecordSet,
  Secret,
  CloudflareIdentity,
  Route53Identity,
  Zone,
  ZoneClass,
  ZoneUnit,
} from '../resources';

export type ResourceHandle<T extends KubeObjectInterface> = {
  jsonData: T;
  delete: () => Promise<unknown>;
};

export type ResourceListState<T extends KubeObjectInterface> = {
  items: T[];
  objects: Array<ResourceHandle<T>>;
  loading: boolean;
  error: unknown;
};

export type DnsData = {
  zones: ResourceListState<Zone>;
  zoneClasses: ResourceListState<ZoneClass>;
  recordSets: ResourceListState<RecordSet>;
  zoneUnits: ResourceListState<ZoneUnit>;
  providers: ResourceListState<Provider>;
  identities: ResourceListState<ProviderIdentity>;
  namespaces: ResourceListState<Namespace>;
  events: ResourceListState<KubeEvent>;
};

export type DnsApiPlatform = {
  useDnsData: () => DnsData;
  useAccess: (
    resource: DnsResourceDescriptor,
    verb: string,
    attrs?: Record<string, string | undefined>
  ) => boolean;
  createZone: (manifest: Zone) => Promise<void>;
  updateZone: (manifest: Zone) => Promise<void>;
  requestZoneReconcile: (zone: Zone, value: string) => Promise<void>;
  createRecordSet: (manifest: RecordSet) => Promise<void>;
  updateRecordSet: (manifest: RecordSet) => Promise<void>;
  createRoute53Identity: (manifest: Route53Identity) => Promise<void>;
  updateRoute53Identity: (manifest: Route53Identity) => Promise<void>;
  createCloudflareIdentity: (manifest: CloudflareIdentity) => Promise<void>;
  updateCloudflareIdentity: (manifest: CloudflareIdentity) => Promise<void>;
  createSecret: (manifest: Secret) => Promise<void>;
  updateSecretData: (namespace: string, name: string, key: string, value: string) => Promise<void>;
  createZoneClass: (manifest: ZoneClass) => Promise<void>;
  updateZoneClass: (manifest: ZoneClass) => Promise<void>;
  clipboard: {
    writeText: (value: string) => Promise<void>;
  };
  navigation: {
    push: (dnsPath: string) => void;
    replace: (dnsPath: string) => void;
  };
  externalLinks: {
    open: (url: string) => void;
  };
  notifications: {
    success: (message: string) => void;
    warning: (message: string) => void;
    error: (message: string) => void;
  };
  liveYaml: {
    pathFor: (resource: KubeObjectInterface) => string;
    open: (resource: KubeObjectInterface) => void;
  };
};

const DnsPlatformContext = React.createContext<DnsApiPlatform | null>(null);
let activePlatform: DnsApiPlatform | null = null;

export function DnsPlatformProvider({
  platform,
  children,
}: {
  platform: DnsApiPlatform;
  children: React.ReactNode;
}) {
  activePlatform = platform;
  return <DnsPlatformContext.Provider value={platform}>{children}</DnsPlatformContext.Provider>;
}

export function useDnsPlatform() {
  const platform = React.useContext(DnsPlatformContext);
  if (!platform) {
    throw new Error('DnsApiApp requires a DnsApiPlatform');
  }
  return platform;
}

export function getDnsPlatform() {
  if (!activePlatform) {
    throw new Error('DnsApiApp requires a DnsApiPlatform');
  }
  return activePlatform;
}
