/** @jsxRuntime classic */
import { Alert } from '../../components/primitives';
import { Box } from '../../components/primitives';
import { InputLabel } from '../../components/primitives';
import { MenuItem } from '../../components/primitives';
import { Select } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import React from 'react';
import { z } from 'zod';
import { createRecordSet, updateRecordSet } from '../../api/dns';
import { useDnsPlatform } from '../../platform';
import { nameOf, namespaceOf, zoneClassRefNamespace, zoneRefNamespace } from '../../resources';
import type { Namespace, Provider, RecordSet, Zone } from '../../types/resources';
import {
  type BreadcrumbItem,
  DnsData,
  DnsFormControl,
  DnsTextField,
  FormFieldError,
  FormPanel,
  jsonIndent,
  labelSelectorMatches,
  Page,
  Panel,
  ProviderBadge,
  providerForResource,
  readJSON,
  tableHeaderSx,
  ToolbarButton,
  ui,
  unique,
  useDnsFormState,
  useNotice,
  YamlPreview,
} from '../common/ui';

type RecordWizardState = {
  namespace: string;
  name: string;
  zoneNamespace: string;
  zoneName: string;
  provider: string;
  type: string;
  recordName: string;
  ttl: string;
  addresses: string;
  txtValues: string;
  cnameTarget: string;
  mxRecords: Array<{ preference: string; exchange: string }>;
  mxNull: boolean;
  caaRecords: Array<{ flags: string; tag: string; value: string }>;
  nsNameServers: string;
  aliasEnabled: boolean;
  aliasDNSName: string;
  aliasHostedZoneID: string;
  aliasEvaluateTargetHealth: boolean;
  cloudflareTTLMode: 'fixed' | 'auto';
  cloudflareProxied: boolean;
  cloudflareComment: string;
  cloudflareTags: Array<{ name: string; value: string }>;
  options: string;
  adoption: string;
};

type RecordFormErrors = Partial<Record<keyof RecordWizardState, string>>;

const recordWizardSchema = z.object({
  namespace: z.string(),
  name: z.string(),
  zoneNamespace: z.string(),
  zoneName: z.string(),
  provider: z.string(),
  type: z.string(),
  recordName: z.string(),
  ttl: z.string(),
  addresses: z.string(),
  txtValues: z.string(),
  cnameTarget: z.string(),
  mxRecords: z.array(z.object({ preference: z.string(), exchange: z.string() })),
  mxNull: z.boolean(),
  caaRecords: z.array(z.object({ flags: z.string(), tag: z.string(), value: z.string() })),
  nsNameServers: z.string(),
  aliasEnabled: z.boolean(),
  aliasDNSName: z.string(),
  aliasHostedZoneID: z.string(),
  aliasEvaluateTargetHealth: z.boolean(),
  cloudflareTTLMode: z.enum(['fixed', 'auto']),
  cloudflareProxied: z.boolean(),
  cloudflareTags: z.array(z.object({ name: z.string(), value: z.string() })),
  cloudflareComment: z.string(),
  options: z.string(),
  adoption: z.string(),
});

const standardRecordTypes = ['A', 'AAAA', 'TXT', 'CNAME', 'MX', 'CAA', 'NS'];
const route53RecordTypes = ['A', 'AAAA', 'TXT', 'CNAME', 'MX', 'CAA', 'NS'];

function availableRecordTypesForForm(
  provider: Provider | undefined,
  providerRef: string,
  isRoute53: boolean
) {
  const version = providerVersionForRef(provider, providerRef);
  const supportedTypes = version?.recordSet?.supportedTypes?.length
    ? version.recordSet.supportedTypes
    : standardRecordTypes;
  const allowedTypes = isRoute53 ? route53RecordTypes : standardRecordTypes;
  const availableTypes = supportedTypes.filter(type => allowedTypes.includes(type));
  return availableTypes.length ? availableTypes : standardRecordTypes;
}

function providerNameFromRef(ref?: string | { name?: string; version?: string }) {
  if (typeof ref === 'string') {
    return ref.split('/')[0] ?? '';
  }
  return ref?.name ?? '';
}

function providerVersionForRef(provider?: Provider, ref?: string | { name?: string; version?: string }) {
  const versionName = typeof ref === 'string' ? ref.split('/')[1] ?? '' : ref?.version ?? '';
  return provider?.spec.versions.find(version => version.name === versionName);
}

function providerRefString(ref?: string | { name?: string; version?: string }) {
  if (!ref) {
    return '';
  }
  if (typeof ref === 'string') {
    return ref;
  }
  return `${ref.name ?? ''}/${ref.version ?? ''}`;
}

function providerRefObject(ref: string): RecordSet['spec']['provider'] {
  const [name, version] = ref.split('/');
  return { name: name ?? '', version: version ?? '' };
}

function buildRecordSetManifest(rawForm: RecordWizardState): RecordSet {
  const form = recordWizardSchema.parse(rawForm);
  const namespace = form.namespace.trim();
  const type = form.type.trim();
  const recordName = form.recordName.trim();
  const resourceName = form.name.trim() || recordSetResourceName(recordName, type);
  const zoneNamespace = form.zoneNamespace.trim();
  const zoneName = form.zoneName.trim();
  const provider = form.provider.trim();
  const isCloudflare = providerNameFromRef(provider) === 'cloudflare.dns.appthrust.io';
  if (!namespace) {
    throw new Error('RecordSet namespace is required');
  }
  if (!zoneName) {
    throw new Error('Zone reference is required');
  }
  if (!provider) {
    throw new Error('Provider is required');
  }
  if (!type) {
    throw new Error('Record type is required');
  }
  if (!recordName) {
    throw new Error('Record name is required');
  }
  if (!resourceName) {
    throw new Error('RecordSet name is required');
  }
  const isAlias = form.aliasEnabled && (type === 'A' || type === 'AAAA');
  const isCloudflareAutomaticTTL = isCloudflare && form.cloudflareTTLMode === 'auto';
  const ttlText = form.ttl.trim();
  const ttl = !isAlias && !isCloudflareAutomaticTTL && ttlText ? Number(ttlText) : undefined;
  if (!isAlias && !isCloudflareAutomaticTTL && !ttlText) {
    throw new Error('TTL is required');
  }
  if (!isAlias && !isCloudflareAutomaticTTL && (!Number.isInteger(ttl) || Number(ttl) <= 0)) {
    throw new Error('TTL must be a positive integer');
  }
  const addresses =
    !isAlias && (type === 'A' || type === 'AAAA')
      ? validateAddressValuesForForm(form.addresses, type)
      : [];
  const txtValues = !isAlias && type === 'TXT' ? validateTXTValuesForForm(form.txtValues) : [];
  const cnameTarget = form.cnameTarget.trim();
  const mxRecords =
    !isAlias && type === 'MX'
      ? form.mxNull && !isCloudflare
        ? [{ preference: 0, exchange: '.' }]
        : validateMXRecordsForForm(form.mxRecords, false)
      : [];
  const caaRecords = !isAlias && type === 'CAA' ? validateCAARecordsForForm(form.caaRecords) : [];
  const nsNameServers = !isAlias && type === 'NS' ? validateNSNameServersForForm(form.nsNameServers) : [];
  if (!isAlias && type === 'A' && !addresses.length) {
    throw new Error('A addresses are required');
  }
  if (!isAlias && type === 'AAAA' && !addresses.length) {
    throw new Error('AAAA addresses are required');
  }
  if (!isAlias && type === 'TXT') {
    validateTXTValuesForForm(form.txtValues);
  }
  if (!isAlias && type === 'CNAME') {
    if (recordName === '@') {
      throw new Error('CNAME records cannot use @ as the record name');
    }
    validateCNAMETargetForForm(cnameTarget);
  }
  if (!isAlias && type === 'MX') {
    validateMXRecordsForForm(form.mxRecords, form.mxNull && !isCloudflare);
  }
  if (!isAlias && type === 'CAA') {
    validateCAARecordsForForm(form.caaRecords);
  }
  if (!isAlias && type === 'NS') {
    if (recordName === '@') {
      throw new Error('Delegated NS records cannot use @ as the record name');
    }
    if (recordName.startsWith('*')) {
      throw new Error('Delegated NS records cannot use wildcard record names');
    }
    validateNSNameServersForForm(form.nsNameServers);
  }
  const aliasDNSName = form.aliasDNSName.trim();
  const aliasHostedZoneID = form.aliasHostedZoneID.trim();
  if (isAlias && (!aliasDNSName || !aliasHostedZoneID)) {
    throw new Error('Alias DNS name and hosted zone ID are required');
  }
  const explicitOptions = readJSON(form.options, undefined, 'options') as
    | Record<string, unknown>
    | undefined;
  const cloudflareOptions = isCloudflare
    ? buildCloudflareOptionsForForm(form, type)
    : undefined;
  const options = isAlias
    ? {
        ...(explicitOptions ?? {}),
        alias: {
          dnsName: aliasDNSName,
          hostedZoneID: aliasHostedZoneID,
          evaluateTargetHealth: form.aliasEvaluateTargetHealth,
        },
      }
    : isCloudflare
      ? cloudflareOptions
      : explicitOptions;
  const adoption = readJSON(form.adoption, undefined, 'adoption');
  const spec: RecordSet['spec'] & Record<string, unknown> = {
    zoneRef: {
      ...(zoneNamespace && zoneNamespace !== namespace ? { namespace: zoneNamespace } : {}),
      name: zoneName,
    },
    provider: providerRefObject(provider),
    type,
    name: recordName,
    ...(ttl ? { ttl } : {}),
    ...(options ? { options } : {}),
    ...(adoption ? { adoption: adoption as Record<string, unknown> } : {}),
  };
  if (!isAlias && type === 'A') {
    spec.a = { addresses };
  } else if (!isAlias && type === 'AAAA') {
    spec.aaaa = { addresses };
  } else if (!isAlias && type === 'TXT') {
    spec.txt = { values: txtValues };
  } else if (!isAlias && type === 'CNAME') {
    spec.cname = { target: cnameTarget };
  } else if (!isAlias && type === 'MX') {
    spec.mx = { records: mxRecords };
  } else if (!isAlias && type === 'CAA') {
    spec.caa = { records: caaRecords };
  } else if (!isAlias && type === 'NS') {
    spec.ns = { nameServers: nsNameServers };
  }
  return {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'RecordSet',
    metadata: {
      namespace,
      name: resourceName,
    } as RecordSet['metadata'],
    spec,
  };
}

function validateRecordSetForm(form: RecordWizardState): RecordFormErrors {
  const errors: RecordFormErrors = {};
  const type = form.type.trim();
  const recordName = form.recordName.trim();
  const resourceName = form.name.trim() || recordSetResourceName(recordName, type);
  const isAlias = form.aliasEnabled && (type === 'A' || type === 'AAAA');
  const isCloudflare = providerNameFromRef(form.provider) === 'cloudflare.dns.appthrust.io';
  const isCloudflareAutomaticTTL = isCloudflare && form.cloudflareTTLMode === 'auto';
  const ttlText = form.ttl.trim();

  if (!form.namespace.trim()) {
    errors.namespace = 'RecordSet namespace is required.';
  }
  if (!type) {
    errors.type = 'Record type is required.';
  }
  if (!recordName) {
    errors.recordName = 'Record name is required.';
  } else if (type === 'CNAME' && recordName === '@') {
    errors.recordName = 'CNAME records cannot use @ as the record name.';
  } else if (type === 'NS' && recordName === '@') {
    errors.recordName = 'Delegated NS records cannot use @ as the record name.';
  } else if (type === 'NS' && recordName.startsWith('*')) {
    errors.recordName = 'Delegated NS records cannot use wildcard record names.';
  }
  if (!resourceName) {
    errors.name = 'RecordSet name is required.';
  }
  if (!isAlias && !isCloudflareAutomaticTTL && !ttlText) {
    errors.ttl = 'TTL is required.';
  } else if (!isAlias && !isCloudflareAutomaticTTL && (!Number.isInteger(Number(ttlText)) || Number(ttlText) <= 0)) {
    errors.ttl = 'TTL must be a positive integer.';
  }
  if (!isAlias && (type === 'A' || type === 'AAAA')) {
    try {
      validateAddressValuesForForm(form.addresses, type);
    } catch (error) {
      errors.addresses = (error as Error).message;
    }
  }
  if (!isAlias && type === 'TXT') {
    try {
      validateTXTValuesForForm(form.txtValues);
    } catch (error) {
      errors.txtValues = (error as Error).message;
    }
  }
  if (!isAlias && type === 'CNAME') {
    try {
      validateCNAMETargetForForm(form.cnameTarget.trim());
    } catch (error) {
      errors.cnameTarget = (error as Error).message;
    }
  }
  if (!isAlias && type === 'MX') {
    try {
      validateMXRecordsForForm(form.mxRecords, form.mxNull && !isCloudflare);
    } catch (error) {
      errors.mxRecords = (error as Error).message;
    }
  }
  if (!isAlias && type === 'CAA') {
    try {
      validateCAARecordsForForm(form.caaRecords);
    } catch (error) {
      errors.caaRecords = (error as Error).message;
    }
  }
  if (!isAlias && type === 'NS') {
    try {
      validateNSNameServersForForm(form.nsNameServers);
    } catch (error) {
      errors.nsNameServers = (error as Error).message;
    }
  }
  if (isAlias) {
    if (!form.aliasDNSName.trim()) {
      errors.aliasDNSName = 'Alias DNS name is required.';
    }
    if (!form.aliasHostedZoneID.trim()) {
      errors.aliasHostedZoneID = 'Alias hosted zone ID is required.';
    }
  }
  if (isCloudflare) {
    try {
      validateCloudflareOptionsForForm(form, type);
    } catch (error) {
      errors.options = (error as Error).message;
    }
  }
  try {
    readJSON(form.options, undefined, 'options');
  } catch (error) {
    errors.options = (error as Error).message;
  }
  try {
    readJSON(form.adoption, undefined, 'adoption');
  } catch (error) {
    errors.adoption = (error as Error).message;
  }

  return errors;
}

function buildCloudflareOptionsForForm(
  form: RecordWizardState,
  type: string
): Record<string, unknown> | undefined {
  validateCloudflareOptionsForForm(form, type);
  const options: Record<string, unknown> = {};
  if (form.cloudflareTTLMode === 'auto') {
    options.ttl = 'Auto';
  }
  if ((type === 'A' || type === 'AAAA' || type === 'CNAME') && form.cloudflareProxied) {
    options.proxied = true;
  }
  const comment = form.cloudflareComment.trim();
  if (comment) {
    options.comment = comment;
  }
  const tags = form.cloudflareTags
    .map(row => ({ name: row.name.trim(), value: row.value.trim() }))
    .filter(row => row.name || row.value)
    .map(row => `${row.name}:${row.value}`);
  if (tags.length) {
    options.tags = tags;
  }
  return Object.keys(options).length ? options : undefined;
}

function validateCloudflareOptionsForForm(form: RecordWizardState, type: string) {
  if (form.cloudflareProxied && !(type === 'A' || type === 'AAAA' || type === 'CNAME')) {
    throw new Error('Cloudflare proxy status is supported only for A, AAAA, and CNAME records.');
  }
  if (form.cloudflareProxied && form.cloudflareTTLMode !== 'auto') {
    throw new Error('Cloudflare proxied records must use automatic TTL.');
  }
  if (form.cloudflareComment.trim().length > 100) {
    throw new Error('Cloudflare comment must be 100 characters or fewer.');
  }
  const seen = new Set<string>();
  for (const [index, row] of form.cloudflareTags.entries()) {
    const name = row.name.trim();
    const value = row.value.trim();
    if (!name && !value) {
      continue;
    }
    if (!/^[A-Za-z0-9_-]{1,32}$/.test(name)) {
      throw new Error(`Cloudflare tag row ${index + 1} name must use letters, digits, _, or -.`);
    }
    if (name.toLowerCase().startsWith('cf-')) {
      throw new Error('Cloudflare tag names starting with cf- are reserved.');
    }
    if (/[\r\n]/.test(value) || value.length > 100) {
      throw new Error(`Cloudflare tag row ${index + 1} value must be one line and 100 characters or fewer.`);
    }
    const key = `${name.toLowerCase()}:${value}`;
    if (seen.has(key)) {
      throw new Error('Cloudflare tags must not contain duplicate name:value pairs.');
    }
    seen.add(key);
  }
}

function parseMultilineValues(value: string): string[] {
  return value
    .split(/\r?\n/)
    .map(item => item.trim())
    .filter(Boolean);
}

function validateAddressValuesForForm(value: string, type: string): string[] {
  const addresses = parseMultilineValues(value);
  if (!addresses.length) {
    throw new Error(`${type} addresses are required.`);
  }
  const seen = new Set<string>();
  for (const address of addresses) {
    if (seen.has(address)) {
      throw new Error(`${type} addresses must not contain duplicate exact lines.`);
    }
    seen.add(address);
  }
  return addresses;
}

function validateTXTValuesForForm(value: string): string[] {
  const values = parseMultilineValues(value);
  if (!values.length) {
    throw new Error('TXT values are required.');
  }
  const seen = new Set<string>();
  for (const [index, value] of values.entries()) {
    if (utf8ByteLength(value) > 4000) {
      throw new Error(`TXT value line ${index + 1} must be 4000 UTF-8 octets or fewer.`);
    }
    if (seen.has(value)) {
      throw new Error('TXT values must not contain duplicate exact strings.');
    }
    seen.add(value);
  }
  return values;
}

function validateCNAMETargetForForm(target: string) {
  if (!target) {
    throw new Error('CNAME target is required.');
  }
  if (target.length > 253) {
    throw new Error('CNAME target must be 253 octets or fewer.');
  }
  if (/^(?:\d{1,3}\.){3}\d{1,3}$/.test(target)) {
    throw new Error('CNAME target must be a DNS name, not an IP address.');
  }
  if (!/^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?|_[a-z0-9]([a-z0-9-]{0,60}[a-z0-9])?))*$/.test(target)) {
    throw new Error('CNAME target must be lowercase ASCII without a trailing dot.');
  }
}

function validateMXRecordsForForm(
  rows: Array<{ preference: string; exchange: string }>,
  nullMX: boolean
): Array<{ preference: number; exchange: string }> {
  if (nullMX) {
    return [{ preference: 0, exchange: '.' }];
  }
  const records = rows
    .map(row => ({
      preferenceText: row.preference.trim(),
      exchange: row.exchange.trim(),
    }))
    .filter(row => row.preferenceText || row.exchange);
  if (!records.length) {
    throw new Error('MX records are required.');
  }
  const seen = new Set<string>();
  return records.map((record, index) => {
    const preference = Number(record.preferenceText);
    if (!Number.isInteger(preference) || preference < 0 || preference > 65535) {
      throw new Error(`MX row ${index + 1} preference must be an integer from 0 to 65535.`);
    }
    if (!record.exchange) {
      throw new Error(`MX row ${index + 1} mail server is required.`);
    }
    if (record.exchange === '.') {
      throw new Error('Use No inbound mail for Null MX.');
    }
    validateMXExchangeForForm(record.exchange, index + 1);
    const key = `${preference}\u0000${record.exchange}`;
    if (seen.has(key)) {
      throw new Error('MX records must not contain duplicate preference and mail server pairs.');
    }
    seen.add(key);
    return { preference, exchange: record.exchange };
  });
}

function validateMXExchangeForForm(exchange: string, rowNumber: number) {
  if (/^(?:\d{1,3}\.){3}\d{1,3}$/.test(exchange)) {
    throw new Error(`MX row ${rowNumber} mail server must be a DNS name, not an IP address.`);
  }
  if (!/^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*$/.test(exchange)) {
    throw new Error(`MX row ${rowNumber} mail server must be lowercase ASCII without a trailing dot.`);
  }
}

function validateCAARecordsForForm(
  rows: Array<{ flags: string; tag: string; value: string }>
): Array<{ flags: number; tag: string; value: string }> {
  const records = rows
    .map(row => ({
      flagsText: row.flags.trim(),
      tag: row.tag.trim(),
      value: row.value.trim(),
    }))
    .filter(row => row.flagsText || row.tag || row.value);
  if (!records.length) {
    throw new Error('CAA records are required.');
  }
  const seen = new Set<string>();
  return records.map((record, index) => {
    const flags = Number(record.flagsText);
    if (!Number.isInteger(flags) || flags < 0 || flags > 255) {
      throw new Error(`CAA row ${index + 1} flags must be an integer from 0 to 255.`);
    }
    if (!record.tag) {
      throw new Error(`CAA row ${index + 1} tag is required.`);
    }
    if (!/^[a-z0-9]+$/.test(record.tag)) {
      throw new Error(`CAA row ${index + 1} tag must be lowercase ASCII alphanumeric.`);
    }
    if (!record.value) {
      throw new Error(`CAA row ${index + 1} value is required.`);
    }
    const key = `${flags}\u0000${record.tag}\u0000${record.value}`;
    if (seen.has(key)) {
      throw new Error('CAA records must not contain duplicate flags, tag, and value tuples.');
    }
    seen.add(key);
    return { flags, tag: record.tag, value: record.value };
  });
}

function validateNSNameServersForForm(value: string): string[] {
  const nameServers = parseMultilineValues(value);
  if (!nameServers.length) {
    throw new Error('Delegated name servers are required.');
  }
  const seen = new Set<string>();
  for (const [index, nameServer] of nameServers.entries()) {
    validateNSNameServerForForm(nameServer, index + 1);
    if (seen.has(nameServer)) {
      throw new Error('Delegated name servers must not contain duplicate exact lines.');
    }
    seen.add(nameServer);
  }
  return nameServers;
}

function validateNSNameServerForForm(nameServer: string, rowNumber: number) {
  if (/^(?:\d{1,3}\.){3}\d{1,3}$/.test(nameServer)) {
    throw new Error(`NS line ${rowNumber} name server must be a DNS name, not an IP address.`);
  }
  if (!/^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*$/.test(nameServer)) {
    throw new Error(`NS line ${rowNumber} name server must be lowercase ASCII without a trailing dot.`);
  }
}

function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

function recordSetResourceName(recordName: string, type: string): string {
  const normalizedName = recordName
    .trim()
    .toLowerCase()
    .replace(/^@$/, 'apex')
    .replace(/^\*\./, 'wildcard-')
    .replace(/\*/g, 'wildcard')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  const normalizedType = type
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return [normalizedName, normalizedType].filter(Boolean).join('-');
}

function recordSetPreview(form: RecordWizardState): {
  manifest: Partial<RecordSet>;
  complete: boolean;
  messages: string[];
} {
  const validationErrors = validateRecordSetForm(form);
  return {
    manifest: buildPartialRecordSetManifest(form),
    complete: Object.keys(validationErrors).length === 0,
    messages: unique(Object.values(validationErrors).filter(Boolean)),
  };
}

function buildPartialRecordSetManifest(form: RecordWizardState): Partial<RecordSet> {
  const namespace = form.namespace.trim();
  const type = form.type.trim();
  const recordName = form.recordName.trim();
  const resourceName = form.name.trim() || recordSetResourceName(recordName, type);
  const zoneNamespace = form.zoneNamespace.trim();
  const zoneName = form.zoneName.trim();
  const provider = form.provider.trim();
  const isCloudflare = providerNameFromRef(provider) === 'cloudflare.dns.appthrust.io';
  const isAlias = form.aliasEnabled && (type === 'A' || type === 'AAAA');
  const isCloudflareAutomaticTTL = isCloudflare && form.cloudflareTTLMode === 'auto';
  const metadata: Record<string, unknown> = {};
  if (resourceName) {
    metadata.name = resourceName;
  }
  if (namespace) {
    metadata.namespace = namespace;
  }

  const spec: Record<string, unknown> = {};
  const zoneRef: Record<string, unknown> = {};
  if (zoneName) {
    zoneRef.name = zoneName;
  }
  if (zoneNamespace && zoneNamespace !== namespace) {
    zoneRef.namespace = zoneNamespace;
  }
  if (Object.keys(zoneRef).length) {
    spec.zoneRef = zoneRef;
  }
  if (provider) {
    spec.provider = provider;
  }
  if (type) {
    spec.type = type;
  }
  if (recordName) {
    spec.name = recordName;
  }

  const ttl = Number(form.ttl.trim());
  if (!isAlias && !isCloudflareAutomaticTTL && Number.isInteger(ttl) && ttl > 0) {
    spec.ttl = ttl;
  }

  const options = buildPartialRecordSetOptions(form, type, provider, isAlias);
  if (options && Object.keys(options).length) {
    spec.options = options;
  }

  if (!isAlias) {
    if ((type === 'A' || type === 'AAAA') && parseMultilineValues(form.addresses).length) {
      spec[type === 'A' ? 'a' : 'aaaa'] = { addresses: parseMultilineValues(form.addresses) };
    } else if (type === 'TXT' && parseMultilineValues(form.txtValues).length) {
      spec.txt = { values: parseMultilineValues(form.txtValues) };
    } else if (type === 'CNAME' && form.cnameTarget.trim()) {
      spec.cname = { target: form.cnameTarget.trim() };
    } else if (type === 'MX') {
      const mxRows = partialMXRecords(form.mxRecords, form.mxNull && !isCloudflare);
      if (mxRows.length) {
        spec.mx = { records: mxRows };
      }
    } else if (type === 'CAA') {
      const caaRows = partialCAARecords(form.caaRecords);
      if (caaRows.length) {
        spec.caa = { records: caaRows };
      }
    } else if (type === 'NS' && parseMultilineValues(form.nsNameServers).length) {
      spec.ns = { nameServers: parseMultilineValues(form.nsNameServers) };
    }
  }

  return {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'RecordSet',
    ...(Object.keys(metadata).length ? { metadata: metadata as RecordSet['metadata'] } : {}),
    ...(Object.keys(spec).length ? { spec: spec as RecordSet['spec'] } : {}),
  };
}

function buildPartialRecordSetOptions(
  form: RecordWizardState,
  type: string,
  provider: string,
  isAlias: boolean
): Record<string, unknown> | undefined {
  const isCloudflare = providerNameFromRef(provider) === 'cloudflare.dns.appthrust.io';
  const options: Record<string, unknown> = {};
  if (isAlias) {
    const alias: Record<string, unknown> = {};
    if (form.aliasDNSName.trim()) {
      alias.dnsName = form.aliasDNSName.trim();
    }
    if (form.aliasHostedZoneID.trim()) {
      alias.hostedZoneID = form.aliasHostedZoneID.trim();
    }
    alias.evaluateTargetHealth = form.aliasEvaluateTargetHealth;
    options.alias = alias;
  }
  if (isCloudflare) {
    if (form.cloudflareTTLMode === 'auto') {
      options.ttl = 'Auto';
    }
    if ((type === 'A' || type === 'AAAA' || type === 'CNAME') && form.cloudflareProxied) {
      options.proxied = true;
    }
    if (form.cloudflareComment.trim()) {
      options.comment = form.cloudflareComment.trim();
    }
    const tags = form.cloudflareTags
      .map(row => ({ name: row.name.trim(), value: row.value.trim() }))
      .filter(row => row.name || row.value)
      .map(row => `${row.name}:${row.value}`);
    if (tags.length) {
      options.tags = tags;
    }
  }
  return Object.keys(options).length ? options : undefined;
}

function partialMXRecords(
  rows: Array<{ preference: string; exchange: string }>,
  nullMX: boolean
): Array<Partial<{ preference: number; exchange: string }>> {
  if (nullMX) {
    return [{ preference: 0, exchange: '.' }];
  }
  return rows
    .map(row => {
      const preference = Number(row.preference.trim());
      return {
        ...(Number.isInteger(preference) ? { preference } : {}),
        ...(row.exchange.trim() ? { exchange: row.exchange.trim() } : {}),
      };
    })
    .filter(row => Object.keys(row).length);
}

function partialCAARecords(
  rows: Array<{ flags: string; tag: string; value: string }>
): Array<Partial<{ flags: number; tag: string; value: string }>> {
  return rows
    .map(row => {
      const flags = Number(row.flags.trim());
      return {
        ...(Number.isInteger(flags) ? { flags } : {}),
        ...(row.tag.trim() ? { tag: row.tag.trim() } : {}),
        ...(row.value.trim() ? { value: row.value.trim() } : {}),
      };
    })
    .filter(row => Object.keys(row).length);
}

function RecordRowRemoveButton({
  disabled,
  onClick,
}: {
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <Box sx={{ minWidth: { md: 132 } }}>
      <Typography
        aria-hidden="true"
        sx={{
          color: 'transparent',
          display: { xs: 'none', md: 'block' },
          fontSize: 14,
          lineHeight: 1.35,
          mb: 0.75,
          userSelect: 'none',
        }}
      >
        Action
      </Typography>
      <ToolbarButton
        label="Remove"
        icon="mdi:delete"
        tone="secondary"
        disabled={disabled}
        onClick={onClick}
      />
    </Box>
  );
}

function recordFormFromRecordSet(
  recordSet: RecordSet,
  zone: Zone,
  provider: string
): RecordWizardState {
  const spec = recordSet.spec as RecordSet['spec'] & {
    options?: {
      alias?: {
        dnsName?: string;
        hostedZoneID?: string;
        evaluateTargetHealth?: boolean;
      };
      ttl?: string;
      proxied?: boolean;
      comment?: string;
      tags?: string[];
    };
  };
  const alias = spec.options?.alias;
  const mxRecords =
    recordSet.spec.mx?.records?.map(record => ({
      preference: record.preference?.toString() ?? '',
      exchange: record.exchange ?? '',
    })) ?? [{ preference: '10', exchange: '' }];
  const mxNull =
    mxRecords.length === 1 && mxRecords[0]?.preference === '0' && mxRecords[0]?.exchange === '.';
  const caaRecords =
    recordSet.spec.caa?.records?.map(record => ({
      flags: record.flags?.toString() ?? '',
      tag: record.tag ?? '',
      value: record.value ?? '',
    })) ?? [{ flags: '0', tag: 'issue', value: '' }];
  return {
    namespace: namespaceOf(recordSet),
    name: nameOf(recordSet),
    zoneNamespace: zoneRefNamespace(recordSet) || namespaceOf(zone),
    zoneName: recordSet.spec.zoneRef.name,
    provider: providerRefString(recordSet.spec.provider) || provider,
    type: recordSet.spec.type,
    recordName: recordSet.spec.name,
    ttl: recordSet.spec.ttl?.toString() ?? '',
    addresses:
      recordSet.spec.a?.addresses?.join('\n') ??
      recordSet.spec.aaaa?.addresses?.join('\n') ??
      '',
    txtValues: recordSet.spec.txt?.values?.join('\n') ?? '',
    cnameTarget: recordSet.spec.cname?.target ?? '',
    mxRecords,
    mxNull,
    caaRecords,
    nsNameServers: recordSet.spec.ns?.nameServers?.join('\n') ?? '',
    aliasEnabled: Boolean(alias),
    aliasDNSName: alias?.dnsName ?? '',
    aliasHostedZoneID: alias?.hostedZoneID ?? '',
    aliasEvaluateTargetHealth: Boolean(alias?.evaluateTargetHealth),
    cloudflareTTLMode: spec.options?.ttl === 'Auto' ? 'auto' : 'fixed',
    cloudflareProxied: Boolean(spec.options?.proxied),
    cloudflareComment: spec.options?.comment ?? '',
    cloudflareTags:
      spec.options?.tags?.map(tag => {
        const splitAt = tag.indexOf(':');
        return splitAt >= 0
          ? { name: tag.slice(0, splitAt), value: tag.slice(splitAt + 1) }
          : { name: tag, value: '' };
      }) ?? [{ name: '', value: '' }],
    options: recordSet.spec.options ? JSON.stringify(recordSet.spec.options, null, jsonIndent) : '',
    adoption: recordSet.spec.adoption ? JSON.stringify(recordSet.spec.adoption, null, jsonIndent) : '',
  };
}

function recordSetNamespaceOptions(
  zone: Zone,
  namespaces: Namespace[],
  currentNamespace?: string
): string[] {
  const options = [namespaceOf(zone)];
  for (const rule of zone.spec.allowedRecordSets ?? []) {
    options.push(
      ...namespaces
        .filter(namespace =>
          labelSelectorMatches(rule.namespaces.selector, namespace.metadata?.labels ?? {})
        )
        .map(nameOf)
    );
  }
  return unique([...options, currentNamespace ?? ''].filter(Boolean));
}

export function RecordSetFormPage({
  zone,
  data,
  recordSet,
  onBack,
  onSaved,
  breadcrumb,
}: {
  zone: Zone;
  data: DnsData;
  recordSet?: RecordSet;
  onBack: () => void;
  onSaved: () => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const { showSuccess, showError, snackbar } = useNotice();
  const platform = useDnsPlatform();
  const isEdit = Boolean(recordSet);
  const zoneClass = data.zoneClasses.items.find(
    item =>
      namespaceOf(item) === zoneClassRefNamespace(zone) &&
      nameOf(item) === zone.spec.zoneClassRef.name
  );
  const selectedProvider =
    data.providers.items.find(item => nameOf(item) === providerNameFromRef(zoneClass?.spec.provider)) ??
    data.providers.items[0];
  const provider = providerForResource(zoneClass);
  const isRoute53 = provider === 'AWS Route 53';
  const providerRef = providerRefString(zoneClass?.spec.provider ?? zone.spec.provider);
  const isCloudflare = providerNameFromRef(providerRef) === 'cloudflare.dns.appthrust.io';
  const availableRecordTypes = availableRecordTypesForForm(selectedProvider, providerRef, isRoute53);
  const recordTypeKey = availableRecordTypes.join('|');
  const namespaceOptions = React.useMemo(
    () =>
      recordSetNamespaceOptions(
        zone,
        data.namespaces.items,
        recordSet ? namespaceOf(recordSet) : undefined
      ),
    [data.namespaces.items, recordSet, zone]
  );
  const initialForm = React.useMemo<RecordWizardState>(
    () =>
      recordSet
        ? recordFormFromRecordSet(recordSet, zone, providerRef)
        : {
            namespace: '',
            name: '',
            zoneNamespace: namespaceOf(zone),
            zoneName: nameOf(zone),
            provider: providerRef || 'route53.dns.appthrust.io/v1alpha1',
            type: '',
            recordName: '',
            ttl: '',
            addresses: '',
            txtValues: '',
            cnameTarget: '',
            mxRecords: [{ preference: '10', exchange: '' }],
            mxNull: false,
            caaRecords: [{ flags: '0', tag: 'issue', value: '' }],
            nsNameServers: '',
            aliasEnabled: false,
            aliasDNSName: '',
            aliasHostedZoneID: '',
            aliasEvaluateTargetHealth: false,
            cloudflareTTLMode: 'fixed',
            cloudflareProxied: false,
            cloudflareComment: '',
            cloudflareTags: [{ name: '', value: '' }],
            options: '',
            adoption: '',
          },
    [providerRef, recordSet, recordTypeKey, zone]
  );
  const [form, setForm] = useDnsFormState<RecordWizardState>(initialForm);
  const [errors, setErrors] = React.useState<RecordFormErrors>({});
  const isAddressRecord = form.type === 'A' || form.type === 'AAAA';
  const isRoute53AliasRecord = isRoute53 && isAddressRecord && form.aliasEnabled;
  const isCloudflareProxyRecord =
    isCloudflare && (form.type === 'A' || form.type === 'AAAA' || form.type === 'CNAME');
  const isCloudflareAutomaticTTL = isCloudflare && form.cloudflareTTLMode === 'auto';
  const currentZoneHostedZoneID = zone.status?.provider?.data?.hostedZoneID;
  const selectableRecordTypes =
    isEdit && !availableRecordTypes.includes(form.type)
      ? [form.type, ...availableRecordTypes]
      : availableRecordTypes;
  const preview = recordSetPreview(form);

  async function save() {
    const validationErrors = validateRecordSetForm(form);
    if (Object.keys(validationErrors).length) {
      setErrors(validationErrors);
      return;
    }
    setErrors({});
    try {
      const manifest = buildRecordSetManifest(form);
      if (recordSet) {
        await updateRecordSet({
          ...recordSet,
          spec: {
            ...manifest.spec,
            zoneRef: recordSet.spec.zoneRef,
            provider: recordSet.spec.provider,
            type: recordSet.spec.type,
            name: recordSet.spec.name,
          },
        });
        showSuccess('RecordSet updated');
      } else {
        await createRecordSet(manifest);
        showSuccess('RecordSet created');
      }
      onSaved();
    } catch (error) {
      showError(error);
    }
  }

  function setMXRecord(index: number, next: { preference?: string; exchange?: string }) {
    setForm({
      ...form,
      mxRecords: form.mxRecords.map((record, recordIndex) =>
        recordIndex === index ? { ...record, ...next } : record
      ),
    });
  }

  function addMXRecord() {
    setForm({
      ...form,
      mxRecords: [...form.mxRecords, { preference: '10', exchange: '' }],
    });
  }

  function removeMXRecord(index: number) {
    const nextRecords = form.mxRecords.filter((_, recordIndex) => recordIndex !== index);
    setForm({
      ...form,
      mxRecords: nextRecords.length ? nextRecords : [{ preference: '10', exchange: '' }],
    });
  }

  function setCAARecord(index: number, next: { flags?: string; tag?: string; value?: string }) {
    setForm({
      ...form,
      caaRecords: form.caaRecords.map((record, recordIndex) =>
        recordIndex === index ? { ...record, ...next } : record
      ),
    });
  }

  function addCAARecord() {
    setForm({
      ...form,
      caaRecords: [...form.caaRecords, { flags: '0', tag: 'issue', value: '' }],
    });
  }

  function removeCAARecord(index: number) {
    const nextRecords = form.caaRecords.filter((_, recordIndex) => recordIndex !== index);
    setForm({
      ...form,
      caaRecords: nextRecords.length ? nextRecords : [{ flags: '0', tag: 'issue', value: '' }],
    });
  }

  function setCloudflareTag(index: number, next: { name?: string; value?: string }) {
    setForm({
      ...form,
      cloudflareTags: form.cloudflareTags.map((tag, tagIndex) =>
        tagIndex === index ? { ...tag, ...next } : tag
      ),
    });
  }

  function addCloudflareTag() {
    setForm({
      ...form,
      cloudflareTags: [...form.cloudflareTags, { name: '', value: '' }],
    });
  }

  function removeCloudflareTag(index: number) {
    const nextTags = form.cloudflareTags.filter((_, tagIndex) => tagIndex !== index);
    setForm({
      ...form,
      cloudflareTags: nextTags.length ? nextTags : [{ name: '', value: '' }],
    });
  }

  function renderRecordDataFields() {
    if (!form.type) {
      return null;
    }
    if (isRoute53AliasRecord) {
      return (
        <Alert severity="info">
          Standard values are replaced by the Route 53 alias target.
        </Alert>
      );
    }
    if (form.type !== 'A') {
      if (form.type === 'AAAA') {
        return (
          <DnsTextField
            label="AAAA addresses"
            placeholder="2001:db8::10"
            value={form.addresses}
            onChange={event => setForm({ ...form, addresses: event.target.value })}
            error={Boolean(errors.addresses)}
            helperText={errors.addresses ?? 'Write one IPv6 address per line.'}
            multiline
            minRows={3}
            fullWidth
          />
        );
      }
      if (form.type === 'TXT') {
        return (
          <DnsTextField
            label="TXT values"
            placeholder="challenge-token"
            value={form.txtValues}
            onChange={event => setForm({ ...form, txtValues: event.target.value })}
            error={Boolean(errors.txtValues)}
            helperText={errors.txtValues ?? 'Write one logical TXT value per line.'}
            multiline
            minRows={3}
            fullWidth
          />
        );
      }
      if (form.type === 'CNAME') {
        return (
          <DnsTextField
            label="CNAME target"
            placeholder="target.example.net"
            value={form.cnameTarget}
            onChange={event => setForm({ ...form, cnameTarget: event.target.value })}
            error={Boolean(errors.cnameTarget)}
            helperText={
              errors.cnameTarget ??
              'Use a DNS name, not an IP address, URL, URI, or Route 53 ALIAS target. Use lowercase ASCII without a trailing dot.'
            }
            fullWidth
          />
        );
      }
      if (form.type === 'MX') {
        return (
          <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
            <Stack spacing={2}>
              {isCloudflare ? null : (
                <Box component="span">
                  <Stack direction="row" spacing={1.5} alignItems="flex-start">
                    <input
                      aria-label="No inbound mail"
                      type="checkbox"
                      checked={form.mxNull}
                      onChange={event =>
                        setForm({
                          ...form,
                          mxNull: event.target.checked,
                          mxRecords: event.target.checked
                            ? [{ preference: '0', exchange: '.' }]
                            : [{ preference: '10', exchange: '' }],
                        })
                      }
                    />
                    <Box>
                      <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                        No inbound mail
                      </Typography>
                      <Typography sx={{ color: ui.faint, fontSize: 13 }}>
                        Creates a Null MX record for this owner name.
                      </Typography>
                    </Box>
                  </Stack>
                </Box>
              )}
              {form.mxNull ? (
                <Alert severity="info">
                  This owner name declares it does not accept email.
                </Alert>
              ) : (
                <Stack spacing={1.5}>
                  {form.mxRecords.map((record, index) => (
                    <Box
                      key={index}
                      sx={{
                        display: 'grid',
                        columnGap: { xs: '8px', md: '16px' },
                        gridTemplateColumns: { xs: '1fr', md: '140px 1fr auto' },
                        alignItems: 'start',
                        rowGap: '10px',
                      }}
                    >
                      <DnsTextField
                        label="Preference"
                        placeholder="10"
                        value={record.preference}
                        onChange={event => setMXRecord(index, { preference: event.target.value })}
                        error={Boolean(errors.mxRecords)}
                        fullWidth
                      />
                      <DnsTextField
                        label="Mail server"
                        placeholder="mail.example.net"
                        value={record.exchange}
                        onChange={event => setMXRecord(index, { exchange: event.target.value })}
                        error={Boolean(errors.mxRecords)}
                        helperText={
                          index === form.mxRecords.length - 1
                            ? errors.mxRecords ??
                              'Use a DNS name, not an IP address, URL, URI, or Route 53 ALIAS target. dns-api does not verify that it resolves.'
                            : undefined
                        }
                        fullWidth
                      />
                      <RecordRowRemoveButton
                        disabled={form.mxRecords.length === 1}
                        onClick={() => removeMXRecord(index)}
                      />
                    </Box>
                  ))}
                  <Box>
                    <ToolbarButton
                      label="Add row"
                      icon="mdi:plus"
                      tone="secondary"
                      onClick={addMXRecord}
                    />
                  </Box>
                  <Typography sx={{ color: ui.faint, fontSize: 13 }}>
                    Apex MX records are allowed. The mail server may be outside this Zone.
                  </Typography>
                </Stack>
              )}
            </Stack>
          </Panel>
        );
      }
      if (form.type === 'CAA') {
        return (
          <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
            <Stack spacing={2}>
              <Stack spacing={1.5}>
                {form.caaRecords.map((record, index) => (
                  <Box
                    key={index}
                    sx={{
                      display: 'grid',
                      columnGap: { xs: '8px', md: '16px' },
                      gridTemplateColumns: { xs: '1fr', md: '120px 180px 1fr auto' },
                      alignItems: 'start',
                      rowGap: '10px',
                    }}
                  >
                    <DnsTextField
                      label="Flags"
                      placeholder="0"
                      value={record.flags}
                      onChange={event => setCAARecord(index, { flags: event.target.value })}
                      error={Boolean(errors.caaRecords)}
                      fullWidth
                    />
                    <DnsTextField
                      label="Tag"
                      placeholder="issue"
                      value={record.tag}
                      onChange={event => setCAARecord(index, { tag: event.target.value })}
                      error={Boolean(errors.caaRecords)}
                      helperText={
                        index === form.caaRecords.length - 1
                          ? 'Common values: issue, issuewild, iodef.'
                          : undefined
                      }
                      fullWidth
                    />
                    <DnsTextField
                      label="Value"
                      placeholder="letsencrypt.org"
                      value={record.value}
                      onChange={event => setCAARecord(index, { value: event.target.value })}
                      error={Boolean(errors.caaRecords)}
                      helperText={
                        index === form.caaRecords.length - 1
                          ? errors.caaRecords ??
                            'dns-api validates record shape, not CA account parameters or iodef delivery.'
                          : undefined
                      }
                      fullWidth
                    />
                    <RecordRowRemoveButton
                      disabled={form.caaRecords.length === 1}
                      onClick={() => removeCAARecord(index)}
                    />
                  </Box>
                ))}
                <Box>
                  <ToolbarButton
                    label="Add row"
                    icon="mdi:plus"
                    tone="secondary"
                    onClick={addCAARecord}
                  />
                </Box>
                <Typography sx={{ color: ui.faint, fontSize: 13 }}>
                  Apex CAA records are allowed. CAA controls which certificate authorities may
                  issue certificates for this owner name.
                </Typography>
              </Stack>
            </Stack>
          </Panel>
        );
      }
      if (form.type === 'NS') {
        return (
          <DnsTextField
            label="Name servers"
            placeholder="ns-111.example-dns.net"
            value={form.nsNameServers}
            onChange={event => setForm({ ...form, nsNameServers: event.target.value })}
            error={Boolean(errors.nsNameServers)}
            helperText={
              errors.nsNameServers ??
              'Write one delegated name server per line. Use DNS names, not IP addresses, URLs, URIs, or Route 53 ALIAS targets. Delegates this child name; this is not the Zone name server list. dns-api does not verify authority, reachability, glue, or management.'
            }
            multiline
            minRows={3}
            fullWidth
          />
        );
      }
      return (
        <Alert severity="info">
          This record type is not available as a standard body in the initial API.
        </Alert>
      );
    }
    return (
      <DnsTextField
        label="A addresses"
        placeholder="192.0.2.10"
        value={form.addresses}
        onChange={event => setForm({ ...form, addresses: event.target.value })}
        error={Boolean(errors.addresses)}
        helperText={errors.addresses ?? 'Write one IPv4 address per line.'}
        multiline
        minRows={3}
        fullWidth
      />
    );
  }

  return (
    <Page
      breadcrumb={breadcrumb}
      title={isEdit ? 'Edit RecordSet' : 'New RecordSet'}
      description={`Manage ${
        form.type ? `a ${form.type}` : 'a'
      } RecordSet inside ${zone.spec.domainName}.`}
      actions={
        <ToolbarButton
          label="Cancel"
          icon="mdi:close"
          tone="secondary"
          onClick={onBack}
        />
      }
    >
      <FormPanel
        title="Zone"
        footer={
          <ToolbarButton
            label={isEdit ? 'Save changes' : 'Save RecordSet'}
            icon="mdi:check"
            disabled={!preview.complete}
            onClick={save}
          />
        }
      >
        <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
          <Stack spacing={1}>
            <Stack direction="row" spacing={1} alignItems="center" useFlexGap flexWrap="wrap">
              <ProviderBadge provider={provider} />
              <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                {namespaceOf(zone)}/{nameOf(zone)}
              </Typography>
            </Stack>
            <Typography sx={{ color: ui.faint, fontSize: 13 }}>{zone.spec.domainName}</Typography>
            <Box
              sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' } }}
            >
              <Box>
                <Typography sx={tableHeaderSx}>Provider</Typography>
                <Typography sx={{ color: ui.text, fontSize: 13 }}>
                  {form.provider}
                </Typography>
              </Box>
            </Box>
          </Stack>
        </Panel>

        <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
          <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
            RecordSet spec
          </Typography>
          <Typography sx={{ color: ui.faint, fontSize: 13, mt: 0.75 }}>
            Choose the DNS type first. The data fields below change to match the selected type.
          </Typography>
        </Panel>
        <DnsFormControl fullWidth>
          <InputLabel>RecordSet namespace</InputLabel>
          <Select
            inputProps={{ 'aria-label': 'RecordSet namespace' }}
            value={form.namespace}
            error={Boolean(errors.namespace)}
            disabled={isEdit || namespaceOptions.length === 0}
            renderValue={value =>
              value ? (
                String(value)
              ) : (
                <Typography component="span" sx={{ color: ui.faint }}>
                  Select namespace
                </Typography>
              )
            }
            onChange={event => setForm({ ...form, namespace: event.target.value })}
          >
            <MenuItem disabled value="">
              Select namespace
            </MenuItem>
            {namespaceOptions.map(namespace => (
              <MenuItem key={namespace} value={namespace}>
                {namespace}
              </MenuItem>
            ))}
          </Select>
          <FormFieldError>{errors.namespace}</FormFieldError>
        </DnsFormControl>
        <DnsFormControl fullWidth>
          <InputLabel>Record type</InputLabel>
          <Select
            inputProps={{ 'aria-label': 'Record type' }}
            label="Record type"
            value={form.type}
            error={Boolean(errors.type)}
            disabled={isEdit}
            renderValue={value =>
              value ? (
                String(value)
              ) : (
                <Typography component="span" sx={{ color: ui.faint }}>
                  Select record type
                </Typography>
              )
            }
            onChange={event => {
              const nextType = String(event.target.value);
              setForm({
                ...form,
                type: nextType,
                aliasEnabled: false,
              });
            }}
          >
            <MenuItem disabled value="">
              Select record type
            </MenuItem>
            {selectableRecordTypes.map(type => (
              <MenuItem key={type} value={type}>
                {type}
              </MenuItem>
            ))}
          </Select>
          <FormFieldError>{errors.type}</FormFieldError>
        </DnsFormControl>
        <DnsTextField
          label="Record name"
          placeholder="www"
          value={form.recordName}
          onChange={event => setForm({ ...form, recordName: event.target.value })}
          error={Boolean(errors.recordName)}
          helperText={errors.recordName}
          disabled={isEdit}
          fullWidth
        />
        {isRoute53AliasRecord || isCloudflareAutomaticTTL ? (
          <Alert severity="info">
            TTL is omitted because the selected provider option supplies provider-specific TTL behavior.
          </Alert>
        ) : (
          <DnsTextField
            label="TTL"
            placeholder="300"
            value={form.ttl}
            onChange={event => setForm({ ...form, ttl: event.target.value })}
            error={Boolean(errors.ttl)}
            helperText={errors.ttl}
            fullWidth
          />
        )}
        {renderRecordDataFields()}
        {isRoute53 && isAddressRecord ? (
          <Panel sx={{ p: 2 }}>
            <Stack spacing={2}>
              <Box>
                <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                  Provider options
                </Typography>
                <Typography sx={{ color: ui.faint, fontSize: 13, mt: 0.75 }}>
                  These fields are provider-specific options stored in RecordSet spec.options.
                </Typography>
              </Box>
              <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
                <Stack spacing={2}>
                  <Stack direction="row" spacing={1} alignItems="center">
                    <ProviderBadge provider="AWS Route 53" />
                    <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                      Route 53 options
                    </Typography>
                  </Stack>
                  <Box component="span">
                    <Stack direction="row" spacing={1.5} alignItems="flex-start">
                      <input
                        aria-label="Use Route 53 alias target"
                        type="checkbox"
                        checked={isRoute53AliasRecord}
                        onChange={event => setForm({ ...form, aliasEnabled: event.target.checked })}
                      />
                      <Box>
                        <Typography
                          sx={{
                            color: ui.text,
                            fontSize: 14,
                            fontWeight: 600,
                          }}
                        >
                          Use Route 53 alias target
                        </Typography>
                        <Typography sx={{ color: ui.faint, fontSize: 13 }}>
                          Optional for A and AAAA records. Alias records omit standard addresses
                          and TTL.
                        </Typography>
                      </Box>
                    </Stack>
                  </Box>
                  {isRoute53AliasRecord ? (
                    <Stack spacing={2}>
                      <DnsTextField
                        label="Alias target DNS name"
                        placeholder="target.example.com."
                        value={form.aliasDNSName}
                        onChange={event => setForm({ ...form, aliasDNSName: event.target.value })}
                        error={Boolean(errors.aliasDNSName)}
                        helperText={errors.aliasDNSName}
                        fullWidth
                      />
                      <DnsTextField
                        label="Alias target hosted zone ID"
                        placeholder="Z1234567890ABCDEF"
                        value={form.aliasHostedZoneID}
                        onChange={event =>
                          setForm({ ...form, aliasHostedZoneID: event.target.value })
                        }
                        error={Boolean(errors.aliasHostedZoneID)}
                        helperText={
                          errors.aliasHostedZoneID ??
                          'Use the hosted zone ID of the alias target resource. For Elastic Load Balancing, provide the load balancer CanonicalHostedZoneId. This is not normally the dns-api Zone hosted zone ID, except for another record in this hosted zone.'
                        }
                        fullWidth
                      />
                      {currentZoneHostedZoneID ? (
                        <Panel sx={{ bgcolor: ui.panelBgSoft, p: 1.5 }}>
                          <Stack direction="row" spacing={1} alignItems="center" useFlexGap flexWrap="wrap">
                            <Box sx={{ minWidth: 0, flex: '1 1 auto' }}>
                              <Typography sx={tableHeaderSx}>Current Zone hosted zone ID</Typography>
                              <Typography sx={{ color: ui.text, fontSize: 13, wordBreak: 'break-all' }}>
                                {currentZoneHostedZoneID}
                              </Typography>
                            </Box>
                            <ToolbarButton
                              label="Copy"
                              icon="mdi:content-copy"
                              tone="secondary"
                              onClick={() => {
                                void platform.clipboard.writeText(currentZoneHostedZoneID);
                              }}
                            />
                          </Stack>
                        </Panel>
                      ) : null}
                      <Box component="span">
                        <Stack direction="row" spacing={1} alignItems="center">
                          <input
                            aria-label="Evaluate target health"
                            type="checkbox"
                            checked={form.aliasEvaluateTargetHealth}
                            onChange={event =>
                              setForm({
                                ...form,
                                aliasEvaluateTargetHealth: event.target.checked,
                              })
                            }
                          />
                          <Typography sx={{ color: ui.text, fontSize: 13 }}>
                            Evaluate target health
                          </Typography>
                        </Stack>
                      </Box>
                    </Stack>
                  ) : null}
                </Stack>
              </Panel>
            </Stack>
          </Panel>
        ) : null}
        {isCloudflare && form.type ? (
          <Panel sx={{ p: 2 }}>
            <Stack spacing={2}>
              <Box>
                <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                  Cloudflare options
                </Typography>
                <Typography sx={{ color: ui.faint, fontSize: 13, mt: 0.75 }}>
                  These fields are stored in RecordSet spec.options for Cloudflare DNS.
                </Typography>
              </Box>
              <DnsFormControl fullWidth>
                <InputLabel>TTL mode</InputLabel>
                <Select
                  inputProps={{ 'aria-label': 'TTL mode' }}
                  label="TTL mode"
                  value={form.cloudflareTTLMode}
                  onChange={event => {
                    const nextMode = String(event.target.value) as 'fixed' | 'auto';
                    setForm({
                      ...form,
                      cloudflareTTLMode: nextMode,
                      cloudflareProxied:
                        nextMode === 'fixed' ? false : form.cloudflareProxied,
                    });
                  }}
                >
                  <MenuItem value="fixed">Fixed TTL</MenuItem>
                  <MenuItem value="auto">Automatic</MenuItem>
                </Select>
              </DnsFormControl>
              {isCloudflareProxyRecord ? (
                <Box component="span">
                  <Stack direction="row" spacing={1.5} alignItems="flex-start">
                    <input
                      aria-label="Proxy status"
                      type="checkbox"
                      checked={form.cloudflareProxied}
                      onChange={event =>
                        setForm({
                          ...form,
                          cloudflareProxied: event.target.checked,
                          cloudflareTTLMode: event.target.checked ? 'auto' : form.cloudflareTTLMode,
                        })
                      }
                    />
                    <Box>
                      <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                        Proxy status
                      </Typography>
                      <Typography sx={{ color: ui.faint, fontSize: 13 }}>
                        Proxied records use Cloudflare automatic TTL.
                      </Typography>
                    </Box>
                  </Stack>
                </Box>
              ) : null}
              <DnsTextField
                label="Comment"
                placeholder="app endpoint"
                value={form.cloudflareComment}
                onChange={event => setForm({ ...form, cloudflareComment: event.target.value })}
                error={Boolean(errors.options)}
                helperText="Copied to each Cloudflare DNS record managed by this RecordSet."
                fullWidth
              />
              <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
                <Stack spacing={1.5}>
                  {form.cloudflareTags.map((tag, index) => (
                    <Box
                      key={index}
                      sx={{
                        display: 'grid',
                        columnGap: { xs: '8px', md: '16px' },
                        gridTemplateColumns: { xs: '1fr', md: '220px 1fr auto' },
                        alignItems: 'start',
                        rowGap: '10px',
                      }}
                    >
                      <DnsTextField
                        label="Tag name"
                        placeholder="app"
                        value={tag.name}
                        onChange={event => setCloudflareTag(index, { name: event.target.value })}
                        error={Boolean(errors.options)}
                        fullWidth
                      />
                      <DnsTextField
                        label="Tag value"
                        placeholder="frontend"
                        value={tag.value}
                        onChange={event => setCloudflareTag(index, { value: event.target.value })}
                        error={Boolean(errors.options)}
                        helperText={index === form.cloudflareTags.length - 1 ? errors.options : undefined}
                        fullWidth
                      />
                      <RecordRowRemoveButton
                        disabled={form.cloudflareTags.length === 1}
                        onClick={() => removeCloudflareTag(index)}
                      />
                    </Box>
                  ))}
                  <Box>
                    <ToolbarButton
                      label="Add tag"
                      icon="mdi:plus"
                      tone="secondary"
                      onClick={addCloudflareTag}
                    />
                  </Box>
                </Stack>
              </Panel>
            </Stack>
          </Panel>
        ) : null}
        <YamlPreview
          value={preview.manifest}
          complete={preview.complete}
          messages={preview.messages}
        />
      </FormPanel>
      {snackbar}
    </Page>
  );
}
