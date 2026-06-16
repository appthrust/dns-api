import { getDnsPlatform, useDnsPlatform } from '../platform';
import { nameOf, namespaceOf } from '../resources';
import type {
  DnsResourceDescriptor,
  CloudflareIdentity,
  KubeObjectInterface,
  RecordSet,
  Route53Identity,
  Secret,
  Zone,
  ZoneClass,
} from '../resources';
export type { DnsData, ResourceHandle, ResourceListState } from '../platform';

export function useDnsData() {
  return useDnsPlatform().useDnsData();
}

export function useAccess(
  resource: DnsResourceDescriptor,
  verb: string,
  attrs?: Record<string, string | undefined>
) {
  return useDnsPlatform().useAccess(resource, verb, attrs);
}

export async function createZone(manifest: Zone) {
  await getDnsPlatform().createZone(manifest);
}

export async function updateZone(manifest: Zone) {
  await getDnsPlatform().updateZone(manifest);
}

export async function requestZoneReconcile(zone: Zone, value: string) {
  await getDnsPlatform().requestZoneReconcile(zone, value);
}

export async function createRecordSet(manifest: RecordSet) {
  await getDnsPlatform().createRecordSet(manifest);
}

export async function updateRecordSet(manifest: RecordSet) {
  await getDnsPlatform().updateRecordSet(manifest);
}

export async function createRoute53Identity(manifest: Route53Identity) {
  await getDnsPlatform().createRoute53Identity(manifest);
}

export async function updateRoute53Identity(manifest: Route53Identity) {
  await getDnsPlatform().updateRoute53Identity(manifest);
}

export async function createCloudflareIdentity(manifest: CloudflareIdentity) {
  await getDnsPlatform().createCloudflareIdentity(manifest);
}

export async function updateCloudflareIdentity(manifest: CloudflareIdentity) {
  await getDnsPlatform().updateCloudflareIdentity(manifest);
}

export async function createSecret(manifest: Secret) {
  await getDnsPlatform().createSecret(manifest);
}

export async function updateSecretData(namespace: string, name: string, key: string, value: string) {
  await getDnsPlatform().updateSecretData(namespace, name, key, value);
}

export async function createZoneClass(manifest: ZoneClass) {
  await getDnsPlatform().createZoneClass(manifest);
}

export async function updateZoneClass(manifest: ZoneClass) {
  await getDnsPlatform().updateZoneClass(manifest);
}

export async function deleteByKey<T extends KubeObjectInterface>(
  objects: Array<{ jsonData: T; delete: () => Promise<unknown> }>,
  namespace: string,
  name: string
) {
  const object = objects.find(
    item => namespaceOf(item.jsonData) === namespace && nameOf(item.jsonData) === name
  );
  if (!object) {
    throw new Error(`resource ${namespace}/${name} was not found`);
  }
  await object.delete();
}
