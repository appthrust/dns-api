import { describe, expect, it } from 'bun:test';
import { canonicalRecordName } from './dns';

describe('canonicalRecordName', () => {
  it('builds Route 53-style FQDNs from dns-api record names', () => {
    expect(canonicalRecordName('@', 'example.com')).toBe('example.com.');
    expect(canonicalRecordName('*', 'example.com')).toBe('*.example.com.');
    expect(canonicalRecordName('www', 'example.com')).toBe('www.example.com.');
  });
});
