/** @jsxRuntime classic */
import { Box } from '../../components/primitives';
import { MenuItem } from '../../components/primitives';
import { Select } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import React from 'react';
import { z } from 'zod';
import { createZone, updateZone, useDnsData } from '../../api/dns';
import { nameOf, namespaceOf, resourceKey } from '../../resources';
import type { Namespace, Provider, Zone, ZoneClass } from '../../types/resources';
import {
  type BreadcrumbItem,
  ChoiceCard,
  descriptionOf,
  DnsFormControl,
  DnsTextField,
  FixedValue,
  FormFieldError,
  FormPanel,
  labelSelectorMatches,
  namespaceLabelsYaml,
  Page,
  Panel,
  parseNamespaceLabelsYaml,
  ProviderBadge,
  providerForResource,
  selectedBg,
  tableHeaderSx,
  ToolbarButton,
  ui,
  unique,
  useDnsFormState,
  useNotice,
  YamlPreview,
} from '../common/ui';

type ZoneFormState = {
  namespace: string;
  name: string;
  description: string;
  domainName: string;
  zoneClassKey: string;
  zoneMode: 'Application' | 'Shared';
  accessMode: 'sameNamespace' | 'multiNamespace';
  accessLabelKey: string;
  accessLabelValue: string;
  accessPattern: string;
  accessTypes: string;
  accessRules: ZoneAccessRuleForm[];
  adoptionMode: 'create' | 'adopt';
  adoptionHostedZoneID: string;
};

type ZoneFormErrors = Partial<Record<keyof ZoneFormState, string>> & {
  accessRuleLabels?: Record<string, string>;
  accessRecordPatterns?: Record<string, string>;
  accessRecordTypes?: Record<string, string>;
};

type ZoneAccessRuleForm = {
  id: string;
  labels: string;
  records: ZoneAccessRecordForm[];
};

type ZoneAccessRecordForm = {
  id: string;
  pattern: string;
  types: string[];
};

const zoneFormSchema = z.object({
  namespace: z.string(),
  name: z.string(),
  description: z.string(),
  domainName: z.string(),
  zoneClassKey: z.string(),
  zoneMode: z.enum(['Application', 'Shared']),
  accessMode: z.enum(['sameNamespace', 'multiNamespace']),
  accessLabelKey: z.string(),
  accessLabelValue: z.string(),
  accessPattern: z.string(),
  accessTypes: z.string(),
  accessRules: z.array(
    z.object({
      id: z.string(),
      labels: z.string(),
      records: z.array(
        z.object({
          id: z.string(),
          pattern: z.string(),
          types: z.array(z.string()),
        })
      ),
    })
  ),
  adoptionMode: z.enum(['create', 'adopt']),
  adoptionHostedZoneID: z.string(),
});

const zoneFormControlSx = { maxWidth: 620 };
const defaultZoneAccessRecordTypes = ['A', 'AAAA', 'TXT', 'CNAME', 'MX', 'CAA', 'NS'];
const namespaceMatchLabelsPlaceholder = 'appthrust.io/dns-access: enabled\nteam: payments';

function availableZoneAccessRecordTypes(
  zoneClass: ZoneClass,
  providers: Provider[]
): string[] {
  const providerName = zoneClass.spec.provider.name;
  const versionName = zoneClass.spec.provider.version;
  const provider = providers.find(item => nameOf(item) === providerName);
  const version = provider?.spec.versions.find(item => item.name === versionName);
  const supportedTypes = version?.recordSet?.supportedTypes?.length
    ? version.recordSet.supportedTypes
    : defaultZoneAccessRecordTypes;
  const availableTypes = defaultZoneAccessRecordTypes.filter(type => supportedTypes.includes(type));
  return availableTypes.length ? availableTypes : defaultZoneAccessRecordTypes;
}

function deriveZoneNameFromDomain(domainName: string): string {
  return domainName.trim().toLowerCase();
}

let nextZoneFormRowID = 0;

function zoneFormRowID(prefix: string): string {
  nextZoneFormRowID += 1;
  return `${prefix}-${Date.now()}-${nextZoneFormRowID}`;
}

function initialZoneAccessRule(): ZoneAccessRuleForm {
  return {
    id: 'rule-default',
    labels: '',
    records: [{ id: 'record-default', pattern: '', types: [] }],
  };
}

function newZoneAccessRule(): ZoneAccessRuleForm {
  return {
    ...initialZoneAccessRule(),
    id: zoneFormRowID('rule'),
    records: [{ id: zoneFormRowID('record'), pattern: '', types: [] }],
  };
}

function fallbackAccessRuleLabels(form: ZoneFormState): string {
  const key = form.accessLabelKey.trim();
  const value = form.accessLabelValue.trim();
  return key && value ? namespaceLabelsYaml({ [key]: value }) : '';
}

function validateAccessRuleLabels(labels: string, requireLabels: boolean): string | undefined {
  try {
    const matchLabels = parseNamespaceLabelsYaml(labels);
    if (requireLabels && !Object.keys(matchLabels).length) {
      return 'Namespace match labels are required.';
    }
  } catch (error) {
    return `Namespace match labels must be a YAML mapping: ${(error as Error).message}`;
  }
  return undefined;
}

function zoneNamespaceOptions(
  zoneClass: ZoneClass,
  namespaces: Namespace[],
  currentNamespace?: string
): string[] {
  const namespacePolicy = zoneClass.spec.allowedZones.namespaces;
  let options: string[];

  if (namespacePolicy.from === 'All') {
    options = namespaces.map(nameOf);
  } else if (namespacePolicy.from === 'Same') {
    options = [namespaceOf(zoneClass)];
  } else {
    const selector = namespacePolicy.selector ?? {};
    options = namespaces
      .filter(namespace => labelSelectorMatches(selector, namespace.metadata?.labels ?? {}))
      .map(nameOf);
  }

  return unique([...options, currentNamespace ?? ''].filter(Boolean));
}

function zoneFormFromResource(zone: Zone, zoneClass: ZoneClass): ZoneFormState {
  const firstGrant = zone.spec.allowedRecordSets?.[0];
  const firstLabels = firstGrant?.namespaces.selector.matchLabels ?? {};
  const firstRecord = firstGrant?.records?.[0];
  return {
    namespace: namespaceOf(zone),
    name: nameOf(zone),
    description: descriptionOf(zone),
    domainName: zone.spec.domainName,
    zoneClassKey: resourceKey(zoneClass),
    zoneMode: zone.spec.allowedRecordSets?.length ? 'Shared' : 'Application',
    accessMode: zone.spec.allowedRecordSets?.length ? 'multiNamespace' : 'sameNamespace',
    accessLabelKey: Object.keys(firstLabels)[0] ?? '',
    accessLabelValue: Object.values(firstLabels)[0] ?? '',
    accessPattern: firstRecord?.name.pattern ?? '',
    accessTypes: firstRecord?.types.join(', ') ?? '',
    accessRules: zone.spec.allowedRecordSets?.length
      ? zone.spec.allowedRecordSets.map((rule, ruleIndex) => ({
          id: `rule-${ruleIndex}`,
          labels: namespaceLabelsYaml(rule.namespaces.selector.matchLabels ?? {}),
          records: rule.records.map((record, recordIndex) => ({
            id: `record-${ruleIndex}-${recordIndex}`,
            pattern: record.name.pattern,
            types: record.types,
          })),
        }))
      : [initialZoneAccessRule()],
    adoptionMode: zone.spec.adoption ? 'adopt' : 'create',
    adoptionHostedZoneID: zoneAdoptionID(zone) ?? '',
  };
}

function buildZoneManifest(rawForm: ZoneFormState, zoneClass?: ZoneClass): Zone {
  const form = zoneFormSchema.parse(rawForm);
  if (!zoneClass) {
    throw new Error('ZoneClass is required');
  }
  const namespace = form.namespace.trim();
  const name = form.name.trim();
  const domainName = form.domainName.trim();
  if (!namespace) {
    throw new Error('Namespace is required');
  }
  if (!name) {
    throw new Error('Name is required');
  }
  if (!domainName) {
    throw new Error('Domain name is required');
  }
  const manifest: Zone = {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'Zone',
    metadata: {
      namespace,
      name,
      ...(form.description.trim()
        ? { annotations: { 'dns.appthrust.io/description': form.description.trim() } }
        : {}),
    } as unknown as Zone['metadata'],
    spec: {
      domainName,
      provider: zoneClass.spec.provider,
      zoneClassRef: {
        namespace: namespaceOf(zoneClass),
        name: nameOf(zoneClass),
      },
    },
  };

  if (form.zoneMode === 'Shared' || form.accessMode === 'multiNamespace') {
    const ruleForms = form.accessRules.length
      ? form.accessRules
      : [
          {
            id: 'rule-1',
            labels: fallbackAccessRuleLabels(form),
            records: [
              {
                id: 'record-1',
                pattern: form.accessPattern,
                types: form.accessTypes
                  .split(',')
                  .map(item => item.trim())
                  .filter(Boolean),
              },
            ],
          },
        ];
    manifest.spec.allowedRecordSets = ruleForms.map((rule, index) => {
      const matchLabels = parseNamespaceLabelsYaml(rule.labels);
      if (!Object.keys(matchLabels).length) {
        throw new Error(`Record access rule ${index + 1} requires namespace labels`);
      }
      const records = rule.records.map((record, recordIndex) => {
        const types = record.types.filter(Boolean);
        if (!record.pattern.trim() || !types.length) {
          throw new Error(
            `Record access rule ${index + 1}, pattern ${
              recordIndex + 1
            } requires a pattern and type`
          );
        }
        return {
          name: { pattern: record.pattern.trim() },
          types,
        };
      });
      return {
        namespaces: {
          selector: { matchLabels },
        },
        records,
      };
    });
  }

  if (form.adoptionHostedZoneID.trim()) {
    manifest.spec.adoption =
      zoneClass.spec.provider.name === 'cloudflare.dns.appthrust.io'
        ? { zoneID: form.adoptionHostedZoneID.trim() }
        : { hostedZoneId: form.adoptionHostedZoneID.trim() };
  }
  return manifest;
}

function zoneAdoptionID(zone: Zone): string | undefined {
  return (
    (zone.spec.adoption?.hostedZoneId as string | undefined) ??
    (zone.spec.adoption?.zoneID as string | undefined)
  );
}

function validateZoneForm(form: ZoneFormState, zoneClass?: ZoneClass): ZoneFormErrors {
  const errors: ZoneFormErrors = {};

  if (!zoneClass) {
    errors.zoneClassKey = 'ZoneClass is required.';
  }
  if (!form.namespace.trim()) {
    errors.namespace = 'Namespace is required.';
  }
  if (!form.name.trim()) {
    errors.name = 'Name is required.';
  }
  if (!form.domainName.trim()) {
    errors.domainName = 'Domain name is required.';
  }
  if (form.accessMode === 'multiNamespace') {
    for (const rule of form.accessRules) {
      const labelError = validateAccessRuleLabels(rule.labels, true);
      if (labelError) {
        errors.accessRuleLabels = {
          ...(errors.accessRuleLabels ?? {}),
          [rule.id]: labelError,
        };
      }
      for (const record of rule.records) {
        if (!record.pattern.trim()) {
          errors.accessRecordPatterns = {
            ...(errors.accessRecordPatterns ?? {}),
            [record.id]: 'Name pattern is required.',
          };
        }
        if (!record.types.filter(Boolean).length) {
          errors.accessRecordTypes = {
            ...(errors.accessRecordTypes ?? {}),
            [record.id]: 'Choose at least one record type.',
          };
        }
      }
    }
  }
  if (form.adoptionMode === 'adopt' && !form.adoptionHostedZoneID.trim()) {
    errors.adoptionHostedZoneID =
      zoneClass?.spec.provider.name === 'cloudflare.dns.appthrust.io'
        ? 'Zone ID is required.'
        : 'Hosted zone ID is required.';
  }

  return errors;
}

function zonePreview(form: ZoneFormState, zoneClass: ZoneClass): Zone | { error: string } {
  try {
    return buildZoneManifest(form, zoneClass);
  } catch (error) {
    return { error: (error as Error).message };
  }
}

function ZoneFormField({
  label,
  help,
  children,
}: {
  label: string;
  help?: string;
  children: React.ReactNode;
}) {
  return (
    <Stack spacing={1}>
      <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>{label}</Typography>
      {help ? (
        <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.6, maxWidth: 620 }}>
          {help}
        </Typography>
      ) : null}
      {children}
    </Stack>
  );
}

export function ZoneFormPage({
  zoneClass,
  zone,
  onBack,
  onSaved,
  breadcrumb,
}: {
  zoneClass: ZoneClass;
  zone?: Zone;
  onBack: () => void;
  onSaved: () => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const { showSuccess, showError, snackbar } = useNotice();
  const data = useDnsData();
  const isEdit = Boolean(zone);
  const namespaceOptions = React.useMemo(
    () => zoneNamespaceOptions(zoneClass, data.namespaces.items, zone ? namespaceOf(zone) : undefined),
    [data.namespaces.items, zone, zoneClass]
  );
  const initialForm = React.useMemo<ZoneFormState>(
    () =>
      zone
        ? zoneFormFromResource(zone, zoneClass)
        : {
            namespace: '',
            name: '',
            description: '',
            domainName: '',
            zoneClassKey: resourceKey(zoneClass),
            zoneMode: 'Application',
            accessMode: 'sameNamespace',
            accessLabelKey: '',
            accessLabelValue: '',
            accessPattern: '',
            accessTypes: '',
            accessRules: [initialZoneAccessRule()],
            adoptionMode: 'create',
            adoptionHostedZoneID: '',
          },
    [zone, zoneClass]
  );
  const [form, setForm] = useDnsFormState<ZoneFormState>(initialForm);
  const [errors, setErrors] = React.useState<ZoneFormErrors>({});
  const nameEditedRef = React.useRef(isEdit);
  const accessRecordTypes = React.useMemo(
    () => availableZoneAccessRecordTypes(zoneClass, data.providers.items),
    [data.providers.items, zoneClass]
  );
  const isCloudflare = zoneClass.spec.provider.name === 'cloudflare.dns.appthrust.io';
  const providerZoneIDLabel = isCloudflare ? 'Zone ID' : 'Hosted zone ID';
  const providerZoneIDPlaceholder = isCloudflare
    ? '023e105f4ecef8ad9ca31a8372d0c353'
    : 'Z1234567890ABCDEF';
  const preview = zonePreview(
    {
      ...form,
      zoneMode: form.accessMode === 'multiNamespace' ? 'Shared' : 'Application',
      adoptionHostedZoneID: form.adoptionMode === 'adopt' ? form.adoptionHostedZoneID : '',
    },
    zoneClass
  );
  const previewErrors = validateZoneForm(
    {
      ...form,
      zoneMode: form.accessMode === 'multiNamespace' ? 'Shared' : 'Application',
      adoptionHostedZoneID: form.adoptionMode === 'adopt' ? form.adoptionHostedZoneID : '',
    },
    zoneClass
  );
  const previewMessages = Object.values(previewErrors).flatMap(error =>
    typeof error === 'string' ? [error] : Object.values(error ?? {})
  );
  const previewComplete = previewMessages.length === 0 && !('error' in preview);

  function updateRule(ruleID: string, patch: Partial<ZoneAccessRuleForm>) {
    setForm(current => ({
      ...current,
      accessRules: current.accessRules.map(rule =>
        rule.id === ruleID ? { ...rule, ...patch } : rule
      ),
    }));
    if ('labels' in patch) {
      setErrors(current => {
        if (!current.accessRuleLabels?.[ruleID]) {
          return current;
        }
        const nextAccessRuleLabels = { ...current.accessRuleLabels };
        delete nextAccessRuleLabels[ruleID];
        return {
          ...current,
          accessRuleLabels: Object.keys(nextAccessRuleLabels).length
            ? nextAccessRuleLabels
            : undefined,
        };
      });
    }
  }

  function updateRecord(ruleID: string, recordID: string, patch: Partial<ZoneAccessRecordForm>) {
    setForm(current => ({
      ...current,
      accessRules: current.accessRules.map(rule =>
        rule.id === ruleID
          ? {
              ...rule,
              records: rule.records.map(record =>
                record.id === recordID ? { ...record, ...patch } : record
              ),
            }
          : rule
      ),
    }));
  }

  async function save() {
    const validationErrors = validateZoneForm(
      {
        ...form,
        zoneMode: form.accessMode === 'multiNamespace' ? 'Shared' : 'Application',
        adoptionHostedZoneID: form.adoptionMode === 'adopt' ? form.adoptionHostedZoneID : '',
      },
      zoneClass
    );
    if (Object.keys(validationErrors).length) {
      setErrors(validationErrors);
      return;
    }
    setErrors({});
    try {
      const manifest = buildZoneManifest(
        {
          ...form,
          zoneMode: form.accessMode === 'multiNamespace' ? 'Shared' : 'Application',
          adoptionHostedZoneID: form.adoptionMode === 'adopt' ? form.adoptionHostedZoneID : '',
        },
        zoneClass
      );
      if (zone) {
        const annotations = { ...(zone.metadata?.annotations ?? {}) };
        if (form.description.trim()) {
          annotations['dns.appthrust.io/description'] = form.description.trim();
        } else {
          delete annotations['dns.appthrust.io/description'];
        }
        await updateZone({
          ...zone,
          metadata: {
            ...zone.metadata,
            annotations: Object.keys(annotations).length ? annotations : undefined,
          },
          spec: {
            ...manifest.spec,
            domainName: zone.spec.domainName,
            zoneClassRef: zone.spec.zoneClassRef,
          },
        });
        showSuccess('Zone updated');
      } else {
        await createZone(manifest);
        showSuccess('Zone created');
      }
      onSaved();
    } catch (error) {
      showError(error);
    }
  }

  return (
    <Page
      breadcrumb={breadcrumb}
      title={isEdit ? 'Edit Zone' : 'New Zone'}
      description={
        isEdit
          ? `Edit DNS access settings for ${namespaceOf(zone as Zone)}/${nameOf(zone as Zone)}.`
          : `Create a public DNS zone with ${namespaceOf(zoneClass)}/${nameOf(zoneClass)}.`
      }
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
        title="Selected ZoneClass"
        footer={
          <ToolbarButton
            label={isEdit ? 'Save changes' : 'Save Zone'}
            icon="mdi:check"
            disabled={!previewComplete}
            onClick={save}
          />
        }
      >
        <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
          <Stack spacing={1.25}>
            <Stack direction="row" spacing={1} alignItems="center" useFlexGap flexWrap="wrap">
              <ProviderBadge provider={providerForResource(zoneClass)} />
              <Typography sx={{ color: ui.muted, fontSize: 14, fontWeight: 600 }}>
                {namespaceOf(zoneClass)}/{nameOf(zoneClass)}
              </Typography>
            </Stack>
            {descriptionOf(zoneClass) ? (
              <Typography sx={{ color: ui.muted, fontSize: 13, lineHeight: 1.55 }}>
                {descriptionOf(zoneClass)}
              </Typography>
            ) : null}
            <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.55 }}>
              Public DNS only. Private DNS and VPC selection are not part of this flow.
            </Typography>
          </Stack>
        </Panel>
        <ZoneFormField label="Provider">
          <FixedValue value={`${zoneClass.spec.provider.name}/${zoneClass.spec.provider.version}`} />
        </ZoneFormField>
        <ZoneFormField
          label="Domain name"
          help="The apex of the public DNS zone. Use lowercase ASCII without a trailing dot."
        >
          <DnsTextField
            inputProps={{ 'aria-label': 'Domain name' }}
            placeholder="apps.example.com"
            value={form.domainName}
            onChange={event => {
              const domainName = event.target.value;
              setForm(current => ({
                ...current,
                domainName,
                name: nameEditedRef.current ? current.name : deriveZoneNameFromDomain(domainName),
              }));
            }}
            disabled={isEdit}
            error={Boolean(errors.domainName)}
            helperText={errors.domainName}
            sx={zoneFormControlSx}
            fullWidth
          />
        </ZoneFormField>
        <ZoneFormField label="Namespace">
          <DnsFormControl sx={zoneFormControlSx} fullWidth>
            <Select
              inputProps={{ 'aria-label': 'Namespace' }}
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
        </ZoneFormField>
        <ZoneFormField label="Name">
          <DnsTextField
            inputProps={{ 'aria-label': 'Name' }}
            placeholder="apps.example.com"
            value={form.name}
            onChange={event => {
              nameEditedRef.current = true;
              setForm({ ...form, name: event.target.value });
            }}
            disabled={isEdit}
            error={Boolean(errors.name)}
            helperText={errors.name}
            sx={zoneFormControlSx}
            fullWidth
          />
        </ZoneFormField>
        <ZoneFormField label="Description">
          <DnsTextField
            inputProps={{ 'aria-label': 'Description' }}
            placeholder="Short purpose of this Zone"
            value={form.description}
            onChange={event => setForm({ ...form, description: event.target.value })}
            sx={zoneFormControlSx}
            fullWidth
          />
        </ZoneFormField>
        <ZoneFormField
          label="Record access permissions"
          help="Choose who can create RecordSets in this Zone. Cross-namespace access requires namespace labels and record rules."
        >
          <DnsFormControl sx={zoneFormControlSx} fullWidth>
            <Select
              inputProps={{ 'aria-label': 'Record access permissions' }}
              value={form.accessMode}
              onChange={event =>
                setForm({
                  ...form,
                  accessMode: event.target.value as ZoneFormState['accessMode'],
                })
              }
            >
              <MenuItem value="sameNamespace">Only this namespace</MenuItem>
              <MenuItem value="multiNamespace">Allow selected namespaces</MenuItem>
            </Select>
          </DnsFormControl>
        </ZoneFormField>
        {form.accessMode === 'multiNamespace' ? (
          <Panel sx={{ bgcolor: selectedBg, borderColor: ui.accent, p: 2 }}>
            <Stack spacing={2}>
              <Stack direction="row" justifyContent="space-between" alignItems="center">
                <Box>
                  <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                    Allowed RecordSet rules
                  </Typography>
                  <Typography sx={{ color: ui.faint, fontSize: 13, mt: 0.5 }}>
                    Each rule grants selected namespaces access to selected record name patterns and
                    types.
                  </Typography>
                </Box>
                <ToolbarButton
                  label="Add rule"
                  icon="mdi:plus"
                  tone="secondary"
                  onClick={() =>
                    setForm(current => ({
                      ...current,
                      accessRules: [...current.accessRules, newZoneAccessRule()],
                    }))
                  }
                />
              </Stack>
              {form.accessRules.map((rule, ruleIndex) => {
                const accessRuleLabelsError =
                  errors.accessRuleLabels?.[rule.id] ?? validateAccessRuleLabels(rule.labels, false);
                return (
                  <Panel key={rule.id} sx={{ p: 2 }}>
                    <Stack spacing={2}>
                      <Stack direction="row" justifyContent="space-between" alignItems="center">
                        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                          Rule {ruleIndex + 1}
                        </Typography>
                        <ToolbarButton
                          label="Remove"
                          icon="mdi:delete"
                          tone="secondary"
                          disabled={form.accessRules.length === 1}
                          onClick={() =>
                            setForm(current => ({
                              ...current,
                              accessRules: current.accessRules.filter(item => item.id !== rule.id),
                            }))
                          }
                        />
                      </Stack>
                      <ZoneFormField
                        label="Namespace match labels"
                        help="Write labels as a YAML mapping. Empty selectors are not allowed."
                      >
                        <DnsTextField
                          inputProps={{ 'aria-label': 'Namespace match labels' }}
                          placeholder={namespaceMatchLabelsPlaceholder}
                          value={rule.labels}
                          onChange={event => updateRule(rule.id, { labels: event.target.value })}
                          error={Boolean(accessRuleLabelsError)}
                          helperText={accessRuleLabelsError}
                          multiline
                          minRows={3}
                          sx={zoneFormControlSx}
                          fullWidth
                        />
                      </ZoneFormField>
                      <Stack direction="row" justifyContent="space-between" alignItems="center">
                        <Box>
                          <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                            Record patterns
                          </Typography>
                          <Typography sx={{ color: ui.faint, fontSize: 13, mt: 0.5 }}>
                            Use @ for apex and * for wildcard.
                          </Typography>
                        </Box>
                        <ToolbarButton
                          label="Add pattern"
                          icon="mdi:plus"
                          tone="secondary"
                          onClick={() =>
                            updateRule(rule.id, {
                              records: [
                                ...rule.records,
                                { id: zoneFormRowID('record'), pattern: '', types: [] },
                              ],
                            })
                          }
                        />
                      </Stack>
                      {rule.records.map(record => (
                        <Panel key={record.id} sx={{ bgcolor: ui.fieldBg, p: 2 }}>
                          <Box
                            sx={{
                              display: 'grid',
                              gap: 2,
                              gridTemplateColumns: { xs: '1fr', md: '1fr 1fr auto' },
                            }}
                          >
                            <DnsTextField
                              label="Name pattern"
                              value={record.pattern}
                              onChange={event =>
                                updateRecord(rule.id, record.id, { pattern: event.target.value })
                              }
                              error={Boolean(errors.accessRecordPatterns?.[record.id])}
                              helperText={
                                errors.accessRecordPatterns?.[record.id] ??
                                'Enter only the pattern body.'
                              }
                            />
                            <Stack spacing={1}>
                              <Typography sx={tableHeaderSx}>Record types</Typography>
                              <Stack direction="row" spacing={1.5} useFlexGap flexWrap="wrap">
                                {accessRecordTypes.map(type => (
                                  <Box component="span" key={type}>
                                    <Stack direction="row" spacing={0.75} alignItems="center">
                                      <input
                                        aria-label={type}
                                        type="checkbox"
                                        checked={record.types.includes(type)}
                                        onChange={event => {
                                          const nextTypes = event.target.checked
                                            ? unique([...record.types, type])
                                            : record.types.filter(item => item !== type);
                                          updateRecord(rule.id, record.id, { types: nextTypes });
                                        }}
                                      />
                                      <Typography sx={{ color: ui.text, fontSize: 13 }}>
                                        {type}
                                      </Typography>
                                    </Stack>
                                  </Box>
                                ))}
                              </Stack>
                              <FormFieldError>
                                {errors.accessRecordTypes?.[record.id]}
                              </FormFieldError>
                            </Stack>
                            <Box sx={{ alignSelf: 'end' }}>
                              <ToolbarButton
                                label="Remove"
                                icon="mdi:delete"
                                tone="secondary"
                                disabled={rule.records.length === 1}
                                onClick={() =>
                                  updateRule(rule.id, {
                                    records: rule.records.filter(item => item.id !== record.id),
                                  })
                                }
                              />
                            </Box>
                          </Box>
                        </Panel>
                      ))}
                    </Stack>
                  </Panel>
                );
              })}
            </Stack>
          </Panel>
        ) : null}
        <Box sx={{ maxWidth: 840 }}>
          <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1 }}>
            Adoption
          </Typography>
          <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.6, mb: 1.5 }}>
            Choose whether the controller creates a new public DNS zone or takes over an existing
            provider zone.
          </Typography>
          <Box
            sx={{ display: 'grid', gap: 1.5, gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' } }}
          >
            <ChoiceCard
              selected={form.adoptionMode === 'create'}
              title="Create new public hosted zone"
              description="No external reference is stored."
              onClick={() => setForm({ ...form, adoptionMode: 'create' })}
            />
            <ChoiceCard
              selected={form.adoptionMode === 'adopt'}
              title="Adopt existing provider zone"
              description={`Store an existing ${providerZoneIDLabel} in spec.adoption.`}
              onClick={() => setForm({ ...form, adoptionMode: 'adopt' })}
            />
          </Box>
        </Box>
        {form.adoptionMode === 'adopt' ? (
          <Panel sx={{ bgcolor: selectedBg, borderColor: ui.accent, p: 2 }}>
            <ZoneFormField
              label={providerZoneIDLabel}
              help={`The provider controller verifies that this ID points to a public DNS zone whose name matches Domain name.`}
            >
              <DnsTextField
                inputProps={{ 'aria-label': providerZoneIDLabel }}
                placeholder={providerZoneIDPlaceholder}
                value={form.adoptionHostedZoneID}
                onChange={event => setForm({ ...form, adoptionHostedZoneID: event.target.value })}
                error={Boolean(errors.adoptionHostedZoneID)}
                helperText={errors.adoptionHostedZoneID}
                sx={zoneFormControlSx}
                fullWidth
              />
            </ZoneFormField>
          </Panel>
        ) : null}
        <YamlPreview value={preview} complete={previewComplete} messages={previewMessages} />
      </FormPanel>
      {snackbar}
    </Page>
  );
}
