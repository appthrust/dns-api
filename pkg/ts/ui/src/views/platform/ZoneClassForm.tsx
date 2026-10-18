/** @jsxRuntime classic */
import { Alert } from '../../components/primitives';
import { Box } from '../../components/primitives';
import { Button } from '../../components/primitives';
import { InputLabel } from '../../components/primitives';
import { MenuItem } from '../../components/primitives';
import { Select } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import React from 'react';
import { z } from 'zod';
import { createZoneClass, updateZoneClass, useAccess } from '../../api/dns';
import { nameOf, namespaceOf, resourceKey, zoneClassIdentityName, ZoneClassResource } from '../../resources';
import type { Provider, ProviderIdentity, ZoneClass } from '../../types/resources';
import {
  type BreadcrumbItem,
  descriptionOf,
  DnsFormControl,
  DnsTextField,
  EmptyState,
  FixedValue,
  FormPanel,
  IdentitySummaryCard,
  namespaceLabelsYaml,
  parseNamespaceLabelsYaml,
  readJSON,
  ToolbarButton,
  ui,
  useDnsFormState,
  useNotice,
  YamlPreview,
} from '../common/ui';
import { PlatformContentTitle } from './routes';

type ZoneClassForm = {
  namespace: string;
  name: string;
  description: string;
  provider: string;
  controllerName: string;
  identityName: string;
  allowedFrom: 'Same' | 'Selector' | 'All';
  allowedLabelsText: string;
  tags: string;
  parametersJSON: string;
  zoneType: 'Public' | 'Private';
  zoneCreationPolicy: 'Create' | 'Deny';
  zoneDeletionPolicy: 'Delete' | 'Retain';
  sameNameZonePolicy: 'Allow' | 'Deny';
};

type ZoneClassFormErrors = {
  allowedLabelsText?: string;
  tags?: string;
};

const zoneClassFormSchema = z.object({
  namespace: z.string(),
  name: z.string(),
  description: z.string(),
  provider: z.string(),
  controllerName: z.string(),
  identityName: z.string(),
  allowedFrom: z.enum(['Same', 'Selector', 'All']),
  allowedLabelsText: z.string(),
  tags: z.string(),
  parametersJSON: z.string(),
  zoneType: z.enum(['Public', 'Private']),
  zoneCreationPolicy: z.enum(['Create', 'Deny']),
  zoneDeletionPolicy: z.enum(['Delete', 'Retain']),
  sameNameZonePolicy: z.enum(['Allow', 'Deny']),
});

const formHelperSx = { color: ui.faint, fontSize: 12, lineHeight: 1.6, mt: 1 };
const allowedZoneNamespaceSelectorPlaceholder = 'appthrust.io/dns-access: enabled\nteam: payments';
const route53TagsPlaceholder = 'environment: prod\nowner: platform';

function zoneClassMetadataFromForm(form: ZoneClassForm): ZoneClass['metadata'] {
  return {
    namespace: form.namespace,
    name: form.name,
    ...(form.description.trim()
      ? { annotations: { 'dns.appthrust.io/description': form.description.trim() } }
      : {}),
  } as unknown as ZoneClass['metadata'];
}

function zoneClassParametersFromForm(
  form: ZoneClassForm,
  baseParameters: ZoneClass['spec']['parameters'] = {}
): ZoneClass['spec']['parameters'] {
  const tags = parseNamespaceLabelsYaml(form.tags);
  const isPrivateZone = isRoute53ProviderRef(form.provider) && form.zoneType === 'Private';
  const parameters: ZoneClass['spec']['parameters'] = {
    ...baseParameters,
    zoneCreationPolicy: isPrivateZone ? 'Deny' : form.zoneCreationPolicy,
    zoneDeletionPolicy: isPrivateZone ? 'Retain' : form.zoneDeletionPolicy,
  };
  delete (parameters as Record<string, unknown>).identityRef;
  if (isPrivateZone) {
    parameters.zoneType = 'Private';
  } else {
    delete parameters.zoneType;
  }
  if (isRoute53ProviderRef(form.provider)) {
    parameters.tags = tags;
    if (parameters.zoneCreationPolicy === 'Create') {
      parameters.sameNameZonePolicy = form.sameNameZonePolicy;
    } else {
      delete parameters.sameNameZonePolicy;
    }
  } else {
    delete parameters.tags;
    delete parameters.sameNameZonePolicy;
  }
  return parameters;
}

function buildZoneClassDesiredManifest({
  form: rawForm,
  provider,
  providerDefinition,
  baseParameters,
  validateSelector = true,
}: {
  form: ZoneClassForm;
  provider: string;
  providerDefinition?: Provider;
  baseParameters?: ZoneClass['spec']['parameters'];
  validateSelector?: boolean;
}): ZoneClass {
  const form = zoneClassFormSchema.parse(rawForm);
  if (!provider) {
    throw new Error('Provider is required');
  }
  if (!form.controllerName) {
    throw new Error('Controller name is required');
  }
  const providerObject = providerRefObject(provider);
  const version = nameOf(providerDefinition) === providerObject.name
    ? providerDefinition?.spec.versions.find(item => item.name === providerObject.version)
    : undefined;
  if (!version) {
    throw new Error('Provider version is required');
  }
  return {
    apiVersion: 'dns.appthrust.io/v1alpha1',
    kind: 'ZoneClass',
    metadata: zoneClassMetadataFromForm(form),
    spec: {
      provider: providerObject,
      controllerName: form.controllerName,
      ...(version.identity ? { identityRef: { name: form.identityName } } : {}),
      allowedZones: allowedZonesFromForm(form, { validateSelector }),
      parameters: version.identity
        ? zoneClassParametersFromForm(form, baseParameters)
        : z.record(z.string(), z.unknown()).parse(readJSON(form.parametersJSON, {}, 'Provider parameters')),
    },
  };
}

export function buildZoneClassManifest(
  form: ZoneClassForm,
  provider?: Provider
): ZoneClass {
  return buildZoneClassDesiredManifest({
    form,
    provider: form.provider,
    providerDefinition: provider,
  });
}

function buildUpdatedZoneClassManifest(zoneClass: ZoneClass, form: ZoneClassForm, provider?: Provider): ZoneClass {
  const desiredManifest = buildZoneClassDesiredManifest({
    form,
    provider: providerRefString(zoneClass.spec.provider),
    providerDefinition: provider,
    baseParameters: zoneClass.spec.parameters,
  });
  const annotations = { ...(zoneClass.metadata?.annotations ?? {}) };
  if (form.description.trim()) {
    annotations['dns.appthrust.io/description'] = form.description.trim();
  } else {
    delete annotations['dns.appthrust.io/description'];
  }
  return {
    ...zoneClass,
    metadata: {
      ...zoneClass.metadata,
      namespace: namespaceOf(zoneClass),
      name: nameOf(zoneClass),
      annotations,
    } as ZoneClass['metadata'],
    spec: desiredManifest.spec,
  };
}

function allowedZonesFromForm(
  form: ZoneClassForm,
  { validateSelector = true }: { validateSelector?: boolean } = {}
): ZoneClass['spec']['allowedZones'] {
  if (form.allowedFrom === 'Selector') {
    const matchLabels = parseNamespaceLabelsYaml(form.allowedLabelsText);
    if (validateSelector && !Object.keys(matchLabels).length) {
      throw new Error('Allowed zone namespace selector requires at least one key: value label');
    }
    return {
      namespaces: {
        from: 'Selector',
        selector: {
          matchLabels,
        },
      },
    };
  }
  if (form.allowedFrom === 'All') {
    return {
      namespaces: {
        from: 'All',
      },
    };
  }
  return {
    namespaces: {
      from: 'Same',
    },
  };
}

function validateAllowedLabelsText(text: string, { requireLabels = false } = {}) {
  try {
    const matchLabels = parseNamespaceLabelsYaml(text);
    if (requireLabels && Object.keys(matchLabels).length === 0) {
      return 'Allowed zone namespace selector requires at least one key: value label';
    }
    return undefined;
  } catch (error) {
    return `Allowed zone namespace selector is not valid YAML: ${(error as Error).message}`;
  }
}

function validateTagsText(text: string) {
  try {
    parseNamespaceLabelsYaml(text);
    return undefined;
  } catch (error) {
    return `Tags are not valid YAML: ${(error as Error).message}`;
  }
}

function validateZoneClassForm(form: ZoneClassForm): ZoneClassFormErrors {
  const errors: ZoneClassFormErrors = {};
  if (form.allowedFrom === 'Selector') {
    errors.allowedLabelsText = validateAllowedLabelsText(form.allowedLabelsText, {
      requireLabels: true,
    });
  }
  if (isRoute53ProviderRef(form.provider)) {
    errors.tags = validateTagsText(form.tags);
  }
  return Object.fromEntries(
    Object.entries(errors).filter(([, value]) => Boolean(value))
  ) as ZoneClassFormErrors;
}

function zoneClassPreview(
  form: ZoneClassForm,
  provider: Provider
): ZoneClass | { error: string } {
  try {
    return buildZoneClassDesiredManifest({
      form,
      provider: form.provider,
      providerDefinition: provider,
      validateSelector: false,
    });
  } catch (error) {
    return { error: (error as Error).message };
  }
}

function zoneClassFormFromResource(zoneClass: ZoneClass): ZoneClassForm {
  const selector = zoneClass.spec.allowedZones.namespaces.selector;
  const allowedFrom =
    zoneClass.spec.allowedZones.namespaces.from === 'Selector'
      ? 'Selector'
      : zoneClass.spec.allowedZones.namespaces.from === 'All'
      ? 'All'
      : 'Same';
  return {
    namespace: namespaceOf(zoneClass),
    name: nameOf(zoneClass),
    description: descriptionOf(zoneClass),
    provider: providerRefString(zoneClass.spec.provider),
    controllerName: zoneClass.spec.controllerName ?? '',
    identityName: zoneClassIdentityName(zoneClass),
    allowedFrom,
    allowedLabelsText: namespaceLabelsYaml(selector?.matchLabels),
    tags: namespaceLabelsYaml(zoneClass.spec.parameters.tags),
    parametersJSON: JSON.stringify(zoneClass.spec.parameters, null, 2),
    zoneType: zoneClass.spec.parameters.zoneType ?? 'Public',
    zoneCreationPolicy: zoneClass.spec.parameters.zoneCreationPolicy ?? 'Create',
    zoneDeletionPolicy: zoneClass.spec.parameters.zoneDeletionPolicy ?? 'Retain',
    sameNameZonePolicy: zoneClass.spec.parameters.sameNameZonePolicy ?? 'Deny',
  };
}

function updatedZoneClassPreview(
  zoneClass: ZoneClass,
  form: ZoneClassForm,
  provider?: Provider
): ZoneClass | { error: string } {
  try {
    return buildZoneClassDesiredManifest({
      form,
      provider: providerRefString(zoneClass.spec.provider),
      providerDefinition: provider,
      baseParameters: zoneClass.spec.parameters,
      validateSelector: false,
    });
  } catch (error) {
    return { error: (error as Error).message };
  }
}

export function ZoneClassIdentitySelectPage({
  identities,
  providers,
  onSelectProvider,
  onCancel,
  onSelectIdentity,
  breadcrumb,
}: {
  identities: ProviderIdentity[];
  providers: Provider[];
  onSelectProvider: (provider: Provider) => void;
  onCancel: () => void;
  onSelectIdentity: (identity: ProviderIdentity) => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const credentiallessProviders = providers.filter(provider => {
    const version = provider.spec.versions.find(item => item.served);
    return version && !version.identity;
  });
  return (
    <Stack spacing={2.5}>
      <PlatformContentTitle
        breadcrumb={breadcrumb}
        title={credentiallessProviders.length ? 'Select Provider' : 'Select Provider Identity'}
        description="Choose a provider or Provider Identity for this ZoneClass."
        action={
          <ToolbarButton label="Cancel" icon="mdi:close" tone="secondary" onClick={onCancel} />
        }
      />
      {credentiallessProviders.map(provider => (
        <Button
          key={nameOf(provider)}
          variant="outlined"
          onClick={() => onSelectProvider(provider)}
          sx={{ justifyContent: 'flex-start', textTransform: 'none' }}
        >
          {provider.spec.display.name}
        </Button>
      ))}
      {identities.length ? (
        <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', lg: '1fr 1fr' } }}>
          {identities.map(identity => (
            <Button
              key={resourceKey(identity)}
              variant="outlined"
              onClick={() => onSelectIdentity(identity)}
              sx={{
                borderRadius: 1,
                color: ui.text,
                p: 0,
                textAlign: 'left',
                textTransform: 'none',
              }}
            >
              <Box sx={{ width: '100%' }}>
                <IdentitySummaryCard identity={identity} />
              </Box>
            </Button>
          ))}
        </Box>
      ) : credentiallessProviders.length ? null : (
        <EmptyState
          title="No Provider Identities"
          body="Create a Provider Identity before creating ZoneClasses."
        />
      )}
    </Stack>
  );
}

function providerRef(provider?: Provider) {
  const version = provider?.spec.versions.find(item => item.served) ?? provider?.spec.versions[0];
  return provider && version ? `${nameOf(provider)}/${version.name}` : '';
}

function providerRefObject(ref: string): ZoneClass['spec']['provider'] {
  const [name, version] = ref.split('/');
  return { name: name ?? '', version: version ?? '' };
}

function providerRefString(ref?: ZoneClass['spec']['provider']) {
  return ref ? `${ref.name}/${ref.version}` : '';
}

function isRoute53ProviderRef(provider: string) {
  return provider.startsWith('route53.dns.appthrust.io/');
}

function providerNameForIdentity(identity?: ProviderIdentity) {
  if (identity?.kind === 'CloudflareIdentity') {
    return 'cloudflare.dns.appthrust.io';
  }
  return 'route53.dns.appthrust.io';
}

function providerForIdentity(providers: Provider[], identity?: ProviderIdentity) {
  const providerName = providerNameForIdentity(identity);
  return providers.find(provider => nameOf(provider) === providerName) ?? providers[0];
}

export function ZoneClassFormPage({
  identity,
  zoneClass,
  providers,
  provider,
  namespaces = [],
  onBack,
  onSaved,
  breadcrumb,
}: {
  identity?: ProviderIdentity;
  zoneClass?: ZoneClass;
  providers: Provider[];
  provider?: Provider;
  namespaces?: string[];
  onBack: () => void;
  onSaved: () => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const { showSuccess, showError, snackbar } = useNotice();
  const isEdit = Boolean(zoneClass);
  const selectedIdentityProvider = provider ?? providerForIdentity(providers, identity);
  const initialForm = React.useMemo<ZoneClassForm>(
    () =>
      zoneClass
        ? zoneClassFormFromResource(zoneClass)
        : {
            namespace: identity ? namespaceOf(identity) : 'default',
            name: '',
            description: '',
            provider: providerRef(selectedIdentityProvider),
            controllerName: controllerNameForProvider(selectedIdentityProvider),
            identityName: identity ? nameOf(identity) : '',
            allowedFrom: 'Selector',
            allowedLabelsText: '',
            tags: '',
            parametersJSON: '{}',
            zoneType: 'Public',
            zoneCreationPolicy: 'Create',
            zoneDeletionPolicy: 'Delete',
            sameNameZonePolicy: 'Deny',
          },
    [identity, selectedIdentityProvider, zoneClass]
  );
  const [form, setForm] = useDnsFormState<ZoneClassForm>(initialForm);
  const [errors, setErrors] = React.useState<ZoneClassFormErrors>({});

  const providerObject = providerRefObject(form.provider);
  const selectedProvider = providers.find(item => nameOf(item) === providerObject.name);
  const selectedVersion = selectedProvider?.spec.versions.find(item => item.name === providerObject.version);
  const requiresIdentity = Boolean(selectedVersion?.identity);
  const canSave = useAccess(ZoneClassResource, isEdit ? 'update' : 'create', {
    group: 'dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'zoneclasses',
    namespace: form.namespace,
  });
  const isPrivateZone = isRoute53ProviderRef(form.provider) && form.zoneType === 'Private';
  const preview = zoneClass
    ? updatedZoneClassPreview(zoneClass, form, selectedProvider)
    : selectedProvider
    ? zoneClassPreview(form, selectedProvider)
    : { error: 'Provider is required' };
  const liveAllowedLabelsError =
    form.allowedFrom === 'Selector'
      ? validateAllowedLabelsText(form.allowedLabelsText, { requireLabels: false })
      : undefined;
  const allowedLabelsError = errors.allowedLabelsText ?? liveAllowedLabelsError;
  const liveTagsError = isRoute53ProviderRef(form.provider)
    ? validateTagsText(form.tags)
    : undefined;
  const tagsError = errors.tags ?? liveTagsError;

  async function save() {
    const validationErrors = validateZoneClassForm(form);
    if (Object.keys(validationErrors).length) {
      setErrors(validationErrors);
      return;
    }
    setErrors({});
    try {
      if (zoneClass) {
        await updateZoneClass(buildUpdatedZoneClassManifest(zoneClass, form, selectedProvider));
        showSuccess('ZoneClass updated');
      } else {
        await createZoneClass(buildZoneClassManifest(form, selectedProvider));
        showSuccess('ZoneClass created');
      }
      onSaved();
    } catch (error) {
      showError(error);
    }
  }

  return (
    <Stack spacing={2.5}>
      <PlatformContentTitle
        breadcrumb={breadcrumb}
        title={isEdit ? 'Edit ZoneClass' : 'New ZoneClass'}
        description={
          isEdit
            ? `Configure zone policy for ${form.namespace}/${form.name}.`
            : `Configure zone policy for ${form.namespace}/${form.name || '<name>'}.`
        }
        action={
          <ToolbarButton label="Cancel" icon="mdi:close" tone="secondary" onClick={onBack} />
        }
      />
      <FormPanel
        title={requiresIdentity ? 'Selected Provider Identity' : 'Selected Provider'}
        footer={
          <ToolbarButton
            label={isEdit ? 'Save changes' : 'Save ZoneClass'}
            icon="mdi:check"
            onClick={save}
            disabled={!canSave || !form.namespace || !form.name || !selectedVersion || (requiresIdentity && !form.identityName) || 'error' in preview}
          />
        }
      >
        {requiresIdentity ? identity ? (
          <IdentitySummaryCard identity={identity} />
        ) : (
          <Alert severity="warning">
            {form.identityName
              ? `Provider Identity ${form.namespace}/${form.identityName} is not visible.`
              : 'No Provider Identity selected.'}
          </Alert>
        ) : null}
        <Box>
          <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Namespace</Typography>
          {!requiresIdentity && !isEdit ? (
            <DnsFormControl fullWidth>
              <Select
                value={form.namespace}
                onChange={event => setForm({ ...form, namespace: event.target.value })}
              >
                {namespaces.map(namespace => (
                  <MenuItem key={namespace} value={namespace}>{namespace}</MenuItem>
                ))}
              </Select>
            </DnsFormControl>
          ) : (
            <FixedValue value={form.namespace} />
          )}
          {requiresIdentity ? (
            <Typography sx={{ color: ui.faint, fontSize: 12, mt: 1 }}>
              The ZoneClass is created in the same namespace as the selected Provider Identity.
            </Typography>
          ) : null}
        </Box>
        {isEdit ? (
          <Box>
            <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Name</Typography>
            <FixedValue value={form.name} />
          </Box>
        ) : (
          <DnsTextField
            label="Name"
            placeholder="route53-public"
            value={form.name}
            onChange={event => setForm({ ...form, name: event.target.value })}
            fullWidth
          />
        )}
        <DnsTextField
          label="Description"
          placeholder="Short purpose of this ZoneClass"
          value={form.description}
          onChange={event => setForm({ ...form, description: event.target.value })}
          fullWidth
        />
        <Box>
          <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Provider name</Typography>
          <FixedValue value={providerObject.name || '-'} />
        </Box>
        <Box>
          <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Provider version</Typography>
          <FixedValue value={providerObject.version || '-'} />
        </Box>
        <Box>
          <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Controller name</Typography>
          <FixedValue value={form.controllerName || '-'} />
          <Typography sx={formHelperSx}>
            Provider controller instance that reconciles ZoneUnits for this ZoneClass.
          </Typography>
        </Box>
        {!requiresIdentity ? (
          <DnsTextField
            label="Provider parameters (JSON)"
            helperText="Configure resolver.mode, resolver.servers, and expectedNameservers for External DNS. Admission validates the selected Provider schema."
            value={form.parametersJSON}
            onChange={event => setForm({ ...form, parametersJSON: event.target.value })}
            multiline
            minRows={6}
            fullWidth
          />
        ) : (
          <>
        {isRoute53ProviderRef(form.provider) ? (
          <DnsFormControl fullWidth>
            <InputLabel>Zone type</InputLabel>
            <Select
              label="Zone type"
              value={form.zoneType}
              onChange={event =>
                setForm({
                  ...form,
                  zoneType: event.target.value as ZoneClassForm['zoneType'],
                })
              }
            >
              <MenuItem value="Public">Public</MenuItem>
              <MenuItem value="Private">Private</MenuItem>
            </Select>
          </DnsFormControl>
        ) : null}
        <DnsFormControl fullWidth>
          <InputLabel>Zone creation policy</InputLabel>
          <Select
            label="Zone creation policy"
            value={isPrivateZone ? 'Deny' : form.zoneCreationPolicy}
            disabled={isPrivateZone}
            onChange={event =>
              setForm({
                ...form,
                zoneCreationPolicy: event.target.value as ZoneClassForm['zoneCreationPolicy'],
              })
            }
          >
            <MenuItem value="Create">Create</MenuItem>
            <MenuItem value="Deny">Deny</MenuItem>
          </Select>
        </DnsFormControl>
        <Typography sx={formHelperSx}>
          {isPrivateZone
            ? 'Private zones are adoption-only. dns-api never creates, deletes, or VPC-associates them.'
            : 'Controls whether the provider controller may create public DNS zones. Use Deny when Zones must point to existing public hosted zones with adoption.'}
        </Typography>
        {!isPrivateZone && form.zoneCreationPolicy === 'Create' && isRoute53ProviderRef(form.provider) ? (
          <>
            <DnsFormControl fullWidth>
              <InputLabel>Duplicate hosted zone policy</InputLabel>
              <Select
                label="Duplicate hosted zone policy"
                value={form.sameNameZonePolicy}
                onChange={event =>
                  setForm({
                    ...form,
                    sameNameZonePolicy: event.target.value as ZoneClassForm['sameNameZonePolicy'],
                  })
                }
              >
                <MenuItem value="Deny">Deny</MenuItem>
                <MenuItem value="Allow">Allow</MenuItem>
              </Select>
            </DnsFormControl>
            <Typography sx={formHelperSx}>
              Controls what happens when a public hosted zone with the same domain already exists in
              the provider account.
            </Typography>
          </>
        ) : null}
        <DnsFormControl fullWidth>
          <InputLabel>Zone deletion policy</InputLabel>
          <Select
            label="Zone deletion policy"
            value={isPrivateZone ? 'Retain' : form.zoneDeletionPolicy}
            disabled={isPrivateZone}
            onChange={event =>
              setForm({
                ...form,
                zoneDeletionPolicy: event.target.value as ZoneClassForm['zoneDeletionPolicy'],
              })
            }
          >
            <MenuItem value="Delete">Delete</MenuItem>
            <MenuItem value="Retain">Retain</MenuItem>
          </Select>
        </DnsFormControl>
        <Typography sx={formHelperSx}>
          Controls what the provider controller does with the DNS zone when the Kubernetes Zone
          is deleted.
        </Typography>
        {isRoute53ProviderRef(form.provider) ? (
          <DnsTextField
            label="Tags"
            helperText={
              tagsError ??
              'Optional Route 53 hosted zone tags. Write tags as a YAML mapping.'
            }
            placeholder={route53TagsPlaceholder}
            value={form.tags}
            onChange={event => {
              setErrors(current => ({ ...current, tags: undefined }));
              setForm({ ...form, tags: event.target.value });
            }}
            error={Boolean(tagsError)}
            multiline
            minRows={3}
            fullWidth
          />
        ) : null}
          </>
        )}
        <DnsTextField
          label="Allowed zone namespace selector"
          helperText={
            allowedLabelsError ??
            'Namespaces matching these labels can create Zones with this ZoneClass. Write labels as a YAML mapping.'
          }
          placeholder={allowedZoneNamespaceSelectorPlaceholder}
          value={form.allowedLabelsText}
          onChange={event => {
            setErrors(current => ({ ...current, allowedLabelsText: undefined }));
            setForm({ ...form, allowedFrom: 'Selector', allowedLabelsText: event.target.value });
          }}
          error={Boolean(allowedLabelsError)}
          multiline
          minRows={3}
          fullWidth
        />
        <YamlPreview value={preview} />
      </FormPanel>
      {snackbar}
    </Stack>
  );
}

function controllerNameForProvider(provider?: Provider): string {
  const providerName = nameOf(provider);
  if (providerName === 'route53.dns.appthrust.io') {
    return 'route53.dns.appthrust.io/controller';
  }
  if (providerName === 'cloudflare.dns.appthrust.io') {
    return 'cloudflare.dns.appthrust.io/controller';
  }
  return providerName ? `${providerName}/controller` : '';
}
