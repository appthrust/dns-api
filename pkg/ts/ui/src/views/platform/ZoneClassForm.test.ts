import { describe, expect, it } from 'bun:test';
import type { Provider } from '../../types/resources';
import { buildZoneClassManifest } from './ZoneClassForm';

const provider: Provider = {
  apiVersion: 'dns.appthrust.io/v1alpha1',
  kind: 'Provider',
  metadata: { name: 'fixture.dns.example' },
  spec: {
    display: { name: 'Fixture DNS' },
    versions: [
      {
        name: 'v1identity',
        served: true,
        identity: { resource: { group: 'fixture.dns.example', kind: 'Identity', scope: 'Namespaced' } },
      },
      { name: 'v1credentialless', served: true, storage: true },
    ],
  },
};

function form(version: string): Parameters<typeof buildZoneClassManifest>[0] {
  return {
    namespace: 'platform',
    name: 'customer-dns',
    description: '',
    provider: `fixture.dns.example/${version}`,
    controllerName: 'fixture.dns.example/controller',
    identityName: 'stale-selection',
    allowedFrom: 'Same',
    allowedLabelsText: '',
    tags: '',
    parametersJSON: '{"resolver":{"mode":"Recursive","servers":["192.0.2.53:53"]},"expectedNameservers":["ns.example.net"]}',
    zoneType: 'Public',
    zoneCreationPolicy: 'Create',
    zoneDeletionPolicy: 'Retain',
    sameNameZonePolicy: 'Deny',
  };
}

describe('ZoneClass provider identity contract', () => {
  it('omits identity and writer parameters for the selected credentialless version', () => {
    const manifest = buildZoneClassManifest(form('v1credentialless'), provider);
    expect(manifest.spec.provider.version).toBe('v1credentialless');
    expect('identityRef' in manifest.spec).toBe(false);
    expect(manifest.spec.parameters).toEqual({
      resolver: { mode: 'Recursive', servers: ['192.0.2.53:53'] },
      expectedNameservers: ['ns.example.net'],
    });
  });

  it('retains identity and writer parameters for an identity-backed version', () => {
    const manifest = buildZoneClassManifest(form('v1identity'), provider);
    expect(manifest.spec.identityRef).toEqual({ name: 'stale-selection' });
    expect(manifest.spec.parameters).toEqual({ zoneCreationPolicy: 'Create', zoneDeletionPolicy: 'Retain' });
  });

  it('does not guess credentialless operation when the Provider version is unavailable', () => {
    expect(() => buildZoneClassManifest(form('v1missing'), provider)).toThrow('Provider version is required');
  });

  it('rejects non-object credentialless parameters', () => {
    expect(() => buildZoneClassManifest({ ...form('v1credentialless'), parametersJSON: '[]' }, provider)).toThrow();
  });
});
