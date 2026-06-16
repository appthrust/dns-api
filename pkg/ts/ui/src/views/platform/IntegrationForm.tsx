/** @jsxRuntime classic */
import { Alert } from '../../components/primitives';
import { Box } from '../../components/primitives';
import { MenuItem } from '../../components/primitives';
import { Select } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import React from 'react';
import { z } from 'zod';
import {
  createCloudflareIdentity,
  createRoute53Identity,
  createSecret,
  updateSecretData,
  updateCloudflareIdentity,
  updateRoute53Identity,
  useDnsData,
} from '../../api/dns';
import { nameOf, namespaceOf } from '../../resources';
import type { CloudflareIdentity, Provider, Route53Identity, Secret } from '../../types/resources';
import {
  type BreadcrumbItem,
  ChoiceCard,
  descriptionOf,
  DnsFormControl,
  DnsTextField,
  FixedValue,
  FormPanel,
  Panel,
  ToolbarButton,
  ui,
  useDnsFormState,
  useNotice,
  YamlPreview,
} from '../common/ui';
import { PlatformContentTitle } from './routes';

type IdentityForm = {
  namespace: string;
  name: string;
  description: string;
  accountID: string;
  region: string;
  assumeRoleChain: AssumeRoleRow[];
};

type AssumeRoleRow = {
  roleARN: string;
  sessionName: string;
  externalID: string;
};

type CloudflareIdentityForm = {
  namespace: string;
  name: string;
  description: string;
  secretMode: 'create' | 'existing';
  tokenUpdateMode: 'unchanged' | 'update';
  secretName: string;
  secretKey: string;
  accessToken: string;
};

type SupportedIdentityKind = 'Route53Identity' | 'CloudflareIdentity';

const supportedIdentityKinds: SupportedIdentityKind[] = ['Route53Identity', 'CloudflareIdentity'];

const identityFormSchema = z.object({
  namespace: z.string(),
  name: z.string(),
  description: z.string(),
  accountID: z.string(),
  region: z.string(),
  assumeRoleChain: z.array(
    z.object({
      roleARN: z.string(),
      sessionName: z.string(),
      externalID: z.string(),
    })
  ),
});

const cloudflareIdentityFormSchema = z.object({
  namespace: z.string(),
  name: z.string(),
  description: z.string(),
  secretMode: z.enum(['create', 'existing']),
  tokenUpdateMode: z.enum(['unchanged', 'update']),
  secretName: z.string(),
  secretKey: z.string(),
  accessToken: z.string(),
});

type IdentityFormValidation = {
  namespace?: string;
  name?: string;
  accountID?: string;
  region?: string;
  sessionNames: (string | undefined)[];
};

type CloudflareIdentityFormValidation = {
  namespace?: string;
  name?: string;
  secretName?: string;
  secretKey?: string;
  accessToken?: string;
};

const kubernetesNameLabelPattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const awsAccountIDPattern = /^[0-9]{12}$/;
const awsRegionPattern = /^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/;
const roleSessionNamePattern = /^[A-Za-z0-9+=,.@-]+$/;
const secretKeyPattern = /^[A-Za-z0-9._-]+$/;

function validateKubernetesName(name: string): string | undefined {
  if (!name) {
    return 'Name is required.';
  }
  if (name !== name.trim()) {
    return 'Name cannot start or end with spaces.';
  }
  if (name.length > 253) {
    return 'Name must be 253 characters or fewer.';
  }

  const labels = name.split('.');
  if (labels.some(label => !label)) {
    return 'Name must use non-empty DNS labels separated by dots.';
  }
  if (labels.some(label => label.length > 63)) {
    return 'Each DNS label in Name must be 63 characters or fewer.';
  }
  if (labels.some(label => !kubernetesNameLabelPattern.test(label))) {
    return 'Name must use lowercase letters, numbers, "-", or ".", and each label must start and end with a letter or number.';
  }

  return undefined;
}

function validateAWSAccountID(accountID: string): string | undefined {
  if (!accountID) {
    return 'AWS account ID is required.';
  }
  if (!awsAccountIDPattern.test(accountID)) {
    return 'AWS account ID must be exactly 12 digits.';
  }
  return undefined;
}

function validateAWSRegion(region: string): string | undefined {
  if (!region) {
    return 'AWS region is required.';
  }
  if (region !== region.trim()) {
    return 'AWS region cannot start or end with spaces.';
  }
  if (!awsRegionPattern.test(region)) {
    return 'AWS region must look like ap-northeast-1.';
  }
  return undefined;
}

function validateRoleSessionName(sessionName: string): string | undefined {
  const trimmed = sessionName.trim();
  if (!trimmed) {
    return undefined;
  }
  if (sessionName !== trimmed) {
    return 'Session name cannot start or end with spaces.';
  }
  if (sessionName.length < 2 || sessionName.length > 64) {
    return 'Session name must be 2 to 64 characters if set.';
  }
  if (!roleSessionNamePattern.test(sessionName)) {
    return 'Session name can use letters, numbers, and these symbols: +=,.@-';
  }
  return undefined;
}

function validateSecretKey(secretKey: string): string | undefined {
  const trimmed = secretKey.trim();
  if (!trimmed) {
    return 'Secret key is required.';
  }
  if (secretKey !== trimmed) {
    return 'Secret key cannot start or end with spaces.';
  }
  if (!secretKeyPattern.test(secretKey)) {
    return 'Secret key can use letters, numbers, ".", "_", and "-".';
  }
  return undefined;
}

function validateIdentityForm(form: IdentityForm, isEdit: boolean): IdentityFormValidation {
  return {
    namespace: isEdit || form.namespace ? undefined : 'Namespace is required.',
    name: isEdit ? undefined : validateKubernetesName(form.name),
    accountID: isEdit ? undefined : validateAWSAccountID(form.accountID),
    region: isEdit ? undefined : validateAWSRegion(form.region),
    sessionNames: form.assumeRoleChain.map(role => validateRoleSessionName(role.sessionName)),
  };
}

function validateCloudflareIdentityForm(
  form: CloudflareIdentityForm,
  isEdit: boolean
): CloudflareIdentityFormValidation {
  return {
    namespace: form.namespace ? undefined : 'Namespace is required.',
    name: validateKubernetesName(form.name),
    secretName: validateKubernetesName(form.secretName),
    secretKey: validateSecretKey(form.secretKey),
    accessToken:
      ((!isEdit && form.secretMode === 'create') ||
        (isEdit && form.tokenUpdateMode === 'update')) &&
      !form.accessToken.trim()
        ? 'Cloudflare API token is required.'
        : undefined,
  };
}

function hasValidationErrors(validation: IdentityFormValidation): boolean {
  return Boolean(
    validation.namespace ||
      validation.name ||
      validation.accountID ||
      validation.region ||
      validation.sessionNames.some(Boolean)
  );
}

function hasCloudflareValidationErrors(validation: CloudflareIdentityFormValidation): boolean {
  return Boolean(
    validation.namespace ||
      validation.name ||
      validation.secretName ||
      validation.secretKey ||
      validation.accessToken
  );
}

function route53IdentityMetadataFromForm(form: IdentityForm): Route53Identity['metadata'] {
  return {
    namespace: form.namespace,
    name: form.name,
    ...(form.description.trim()
      ? { annotations: { 'dns.appthrust.io/description': form.description.trim() } }
      : {}),
  } as unknown as Route53Identity['metadata'];
}

function cloudflareIdentityMetadataFromForm(
  form: CloudflareIdentityForm
): CloudflareIdentity['metadata'] {
  return {
    namespace: form.namespace,
    name: form.name,
    ...(form.description.trim()
      ? { annotations: { 'dns.appthrust.io/description': form.description.trim() } }
      : {}),
  } as unknown as CloudflareIdentity['metadata'];
}

function buildRoute53IdentityDesiredManifest(
  rawForm: IdentityForm,
  specBase?: Pick<Route53Identity['spec'], 'accountID' | 'region' | 'credentials'>
): Route53Identity {
  const form = identityFormSchema.parse(rawForm);
  const assumeRoleChain = buildAssumeRoleChain(form.assumeRoleChain);
  return {
    apiVersion: 'route53.dns.appthrust.io/v1alpha1',
    kind: 'Route53Identity',
    metadata: route53IdentityMetadataFromForm(form),
    spec: {
      accountID: specBase?.accountID ?? form.accountID,
      region: specBase?.region ?? form.region,
      credentials: specBase?.credentials ?? { runtime: {} },
      ...(assumeRoleChain ? { assumeRoleChain } : {}),
    },
  };
}

function route53IdentityPreview(form: IdentityForm): Route53Identity | { error: string } {
  try {
    return buildRoute53IdentityDesiredManifest(form);
  } catch (error) {
    return { error: (error as Error).message };
  }
}

function buildCloudflareIdentityDesiredManifest(
  rawForm: CloudflareIdentityForm
): CloudflareIdentity {
  const form = cloudflareIdentityFormSchema.parse(rawForm);
  return {
    apiVersion: 'cloudflare.dns.appthrust.io/v1alpha1',
    kind: 'CloudflareIdentity',
    metadata: cloudflareIdentityMetadataFromForm(form),
    spec: {
      accessToken: {
        secretRef: {
          name: form.secretName.trim(),
          key: form.secretKey.trim(),
        },
      },
    },
  };
}

function buildCloudflareTokenSecretManifest(
  rawForm: CloudflareIdentityForm,
  redacted = false
): Secret {
  const form = cloudflareIdentityFormSchema.parse(rawForm);
  const key = form.secretKey.trim();
  return {
    apiVersion: 'v1',
    kind: 'Secret',
    metadata: {
      namespace: form.namespace,
      name: form.secretName.trim(),
    },
    type: 'Opaque',
    stringData: key
      ? {
          [key]: redacted ? '<redacted>' : form.accessToken.trim(),
        }
      : {},
  };
}

function cloudflareIdentityPreview(
  form: CloudflareIdentityForm
): CloudflareIdentity | { error: string } {
  try {
    return buildCloudflareIdentityDesiredManifest(form);
  } catch (error) {
    return { error: (error as Error).message };
  }
}

function cloudflareCreatePreview(
  form: CloudflareIdentityForm
): CloudflareIdentity | Array<CloudflareIdentity | Secret> | { error: string } {
  try {
    const identity = buildCloudflareIdentityDesiredManifest(form);
    if (form.secretMode !== 'create') {
      return identity;
    }
    return [buildCloudflareTokenSecretManifest(form, true), identity];
  } catch (error) {
    return { error: (error as Error).message };
  }
}

function cloudflareIdentityFormFromResource(identity: CloudflareIdentity): CloudflareIdentityForm {
  return {
    namespace: namespaceOf(identity),
    name: nameOf(identity),
    description: descriptionOf(identity),
    secretMode: 'existing',
    tokenUpdateMode: 'unchanged',
    secretName: identity.spec.accessToken.secretRef.name,
    secretKey: identity.spec.accessToken.secretRef.key,
    accessToken: '',
  };
}

function buildUpdatedCloudflareIdentityManifest(
  identity: CloudflareIdentity,
  form: CloudflareIdentityForm
): CloudflareIdentity {
  return {
    ...identity,
    metadata: {
      ...identity.metadata,
      namespace: namespaceOf(identity),
      name: nameOf(identity),
      annotations: {
        ...(identity.metadata?.annotations ?? {}),
        ...(form.description.trim()
          ? { 'dns.appthrust.io/description': form.description.trim() }
          : {}),
      },
    } as CloudflareIdentity['metadata'],
    spec: {
      accessToken: {
        secretRef: {
          name: form.secretName.trim(),
          key: form.secretKey.trim(),
        },
      },
    },
  };
}

function updatedCloudflareIdentityPreview(
  identity: CloudflareIdentity,
  form: CloudflareIdentityForm
): CloudflareIdentity | Array<CloudflareIdentity | Secret> | { error: string } {
  try {
    const updatedIdentity = buildUpdatedCloudflareIdentityManifest(identity, form);
    if (form.tokenUpdateMode !== 'update') {
      return updatedIdentity;
    }
    return [buildCloudflareTokenSecretManifest(form, true), updatedIdentity];
  } catch (error) {
    return { error: (error as Error).message };
  }
}

function buildAssumeRoleChain(rows: AssumeRoleRow[]): Route53Identity['spec']['assumeRoleChain'] {
  const roles = rows
    .map(role => ({
      roleARN: role.roleARN.trim(),
      ...(role.sessionName.trim() ? { sessionName: role.sessionName.trim() } : {}),
      ...(role.externalID.trim() ? { externalID: role.externalID.trim() } : {}),
    }))
    .filter(role => role.roleARN || role.sessionName || role.externalID);
  for (const [index, role] of roles.entries()) {
    if (!role.roleARN) {
      throw new Error(`Assume role ${index + 1} requires a Role ARN`);
    }
  }
  return roles.length ? roles : undefined;
}

function identityFormFromResource(identity: Route53Identity): IdentityForm {
  return {
    namespace: namespaceOf(identity),
    name: nameOf(identity),
    description: descriptionOf(identity),
    accountID: identity.spec.accountID,
    region: identity.spec.region ?? '',
    assumeRoleChain:
      identity.spec.assumeRoleChain?.map(role => ({
        roleARN: role.roleARN,
        sessionName: role.sessionName ?? '',
        externalID: role.externalID ?? '',
      })) ?? [],
  };
}

function buildUpdatedRoute53IdentityManifest(
  identity: Route53Identity,
  form: IdentityForm
): Route53Identity {
  const assumeRoleChain = buildAssumeRoleChain(form.assumeRoleChain);
  return {
    ...identity,
    metadata: {
      ...identity.metadata,
      namespace: namespaceOf(identity),
      name: nameOf(identity),
      annotations: {
        ...(identity.metadata?.annotations ?? {}),
        ...(form.description.trim()
          ? { 'dns.appthrust.io/description': form.description.trim() }
          : {}),
      },
    } as Route53Identity['metadata'],
    spec: {
      accountID: identity.spec.accountID,
      region: identity.spec.region,
      credentials: identity.spec.credentials,
      ...(assumeRoleChain ? { assumeRoleChain } : {}),
    },
  };
}

function updatedRoute53IdentityPreview(
  identity: Route53Identity,
  form: IdentityForm
): Route53Identity | { error: string } {
  try {
    return buildRoute53IdentityDesiredManifest(form, {
      accountID: identity.spec.accountID,
      region: identity.spec.region,
      credentials: identity.spec.credentials,
    });
  } catch (error) {
    return { error: (error as Error).message };
  }
}

function IntegrationField({
  label,
  help,
  error,
  maxWidth,
  children,
}: {
  label: string;
  help?: string;
  error?: string;
  maxWidth?: number | string;
  children: React.ReactNode;
}) {
  return (
    <Box sx={{ maxWidth, width: '100%' }}>
      <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>{label}</Typography>
      {children}
      {error || help ? (
        <Typography
          sx={{
            color: error ? ui.dangerText : ui.faint,
            fontSize: 12,
            lineHeight: 1.6,
            mt: 1,
          }}
        >
          {error || help}
        </Typography>
      ) : null}
    </Box>
  );
}

function providerDisplayName(provider: Provider): string {
  return provider.spec.display.name || nameOf(provider);
}

function providerIdentityKind(provider: Provider): string {
  return (
    provider.spec.versions.find(version => version.served && version.identity?.resource?.kind)
      ?.identity?.resource?.kind ?? ''
  );
}

function isSupportedIdentityKind(kind: string): kind is SupportedIdentityKind {
  return supportedIdentityKinds.includes(kind as SupportedIdentityKind);
}

function ProviderLogo({ provider }: { provider: Provider }) {
  const logoURL = provider.spec.display.logo?.url;
  if (!logoURL) {
    return null;
  }

  return (
    <Box
      component="img"
      alt=""
      src={logoURL}
      sx={{
        flex: '0 0 auto',
        height: 24,
        objectFit: 'contain',
        width: 24,
      }}
    />
  );
}

function ComingSoon() {
  return (
    <Typography
      component="span"
      sx={{
        border: 1,
        borderColor: ui.border,
        borderRadius: 1,
        color: ui.muted,
        display: 'inline-flex',
        fontSize: 12,
        fontWeight: 600,
        px: 1,
        py: 0.25,
      }}
    >
      Coming soon
    </Typography>
  );
}

export function IntegrationTypeSelectPage({
  onCancel,
  onSelectRoute53,
  onSelectCloudflare,
  breadcrumb,
}: {
  onCancel: () => void;
  onSelectRoute53: () => void;
  onSelectCloudflare: () => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const data = useDnsData();
  const providers = React.useMemo(
    () =>
      [...data.providers.items].sort((a, b) =>
        providerDisplayName(a).localeCompare(providerDisplayName(b))
      ),
    [data.providers.items]
  );

  return (
    <Stack spacing={2.5}>
      <PlatformContentTitle
        breadcrumb={breadcrumb}
        title="Select Provider"
        description="Choose the DNS Provider Identity to add."
        action={
          <ToolbarButton label="Cancel" icon="mdi:close" tone="secondary" onClick={onCancel} />
        }
      />
      <Box
        sx={{
          columnGap: '16px',
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', md: 'repeat(3, 1fr)' },
          rowGap: '16px',
        }}
      >
        {providers.map(provider => {
          const identityKind = providerIdentityKind(provider);
          const supported = isSupportedIdentityKind(identityKind);
          const onSelect =
            identityKind === 'Route53Identity'
              ? onSelectRoute53
              : identityKind === 'CloudflareIdentity'
              ? onSelectCloudflare
              : () => undefined;
          return (
            <ChoiceCard
              key={nameOf(provider)}
              title={providerDisplayName(provider)}
              titlePrefix={<ProviderLogo provider={provider} />}
              description={provider.spec.display.description}
              disabled={!supported}
              onClick={supported ? onSelect : () => undefined}
            />
          );
        })}
        <ChoiceCard
          title="Google Cloud DNS"
          description="Planned provider type."
          disabled
          onClick={() => undefined}
        >
          <ComingSoon />
        </ChoiceCard>
      </Box>
    </Stack>
  );
}

export function CloudflareIdentityFormPage({
  namespaces,
  identity,
  onBack,
  onSaved,
  breadcrumb,
}: {
  namespaces: string[];
  identity?: CloudflareIdentity;
  onBack: () => void;
  onSaved: () => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const { showSuccess, showError, snackbar } = useNotice();
  const isEdit = Boolean(identity);
  const [showValidation, setShowValidation] = React.useState(false);
  const initialForm = React.useMemo<CloudflareIdentityForm>(
    () =>
      identity
        ? cloudflareIdentityFormFromResource(identity)
        : {
            namespace: '',
            name: '',
            description: '',
            secretMode: 'create',
            tokenUpdateMode: 'unchanged',
            secretName: '',
            secretKey: 'api-token',
            accessToken: '',
          },
    [identity]
  );
  const [form, setForm] = useDnsFormState<CloudflareIdentityForm>(initialForm);
  const validation = validateCloudflareIdentityForm(form, isEdit);
  const effectiveValidation = isEdit
    ? { ...validation, namespace: undefined, name: undefined }
    : validation;
  const previewComplete = !hasCloudflareValidationErrors(effectiveValidation);
  const namespaceError = showValidation && !isEdit ? validation.namespace : undefined;
  const nameError = showValidation && !isEdit ? validation.name : undefined;
  const secretNameError = showValidation ? validation.secretName : undefined;
  const secretKeyError = showValidation ? validation.secretKey : undefined;
  const accessTokenError = showValidation ? validation.accessToken : undefined;
  const preview = identity
    ? updatedCloudflareIdentityPreview(identity, form)
    : cloudflareCreatePreview(form);

  async function save() {
    try {
      if (hasCloudflareValidationErrors(effectiveValidation)) {
        setShowValidation(true);
        showError('Fix validation errors before saving.');
        return;
      }
      if (identity) {
        if (form.tokenUpdateMode === 'update') {
          await updateSecretData(
            form.namespace,
            form.secretName.trim(),
            form.secretKey.trim(),
            form.accessToken.trim()
          );
        }
        await updateCloudflareIdentity(buildUpdatedCloudflareIdentityManifest(identity, form));
        showSuccess(
          form.tokenUpdateMode === 'update'
            ? 'Secret and Provider Identity updated'
            : 'Provider Identity updated'
        );
      } else {
        const manifest = cloudflareIdentityPreview(form);
        if ('error' in manifest) {
          throw new Error(String(manifest.error));
        }
        if (form.secretMode === 'create') {
          await createSecret(buildCloudflareTokenSecretManifest(form));
        }
        await createCloudflareIdentity(manifest);
        showSuccess(
          form.secretMode === 'create'
            ? 'Secret and Provider Identity created'
            : 'Provider Identity created'
        );
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
        title={isEdit ? 'Edit Provider Identity' : 'New Provider Identity'}
        description={
          isEdit
            ? `Update credential settings for ${form.namespace}/${form.name}.`
            : 'Create a Cloudflare DNS Provider Identity.'
        }
        action={
          <ToolbarButton label="Cancel" icon="mdi:close" tone="secondary" onClick={onBack} />
        }
      />
      <FormPanel
        title="Provider Identity settings"
        footer={
          <ToolbarButton
            label={isEdit ? 'Save changes' : 'Save Provider Identity'}
            icon="mdi:check"
            onClick={save}
          />
        }
      >
        <Stack spacing={2}>
          {isEdit ? (
            <Box sx={{ flex: 1 }}>
              <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Namespace</Typography>
              <FixedValue value={form.namespace} />
            </Box>
          ) : (
            <IntegrationField label="Namespace" error={namespaceError} maxWidth={620}>
              <DnsFormControl error={Boolean(namespaceError)} fullWidth>
                <Select
                  displayEmpty
                  value={form.namespace}
                  renderValue={value =>
                    value ? (
                      String(value)
                    ) : (
                      <Typography component="span" sx={{ color: ui.faint }}>
                        Select namespace
                      </Typography>
                    )
                  }
                  onChange={event => setForm({ ...form, namespace: String(event.target.value) })}
                >
                  <MenuItem disabled value="">
                    Select namespace
                  </MenuItem>
                  {namespaces.map(namespace => (
                    <MenuItem key={namespace} value={namespace}>
                      {namespace}
                    </MenuItem>
                  ))}
                </Select>
              </DnsFormControl>
            </IntegrationField>
          )}
          {isEdit ? (
            <Box sx={{ flex: 1 }}>
              <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Name</Typography>
              <FixedValue value={form.name} />
            </Box>
          ) : (
            <IntegrationField label="Name" error={nameError} maxWidth={620}>
              <DnsTextField
                placeholder="cloudflare-identity"
                value={form.name}
                onChange={event => setForm({ ...form, name: event.target.value })}
                error={Boolean(nameError)}
                inputProps={{ maxLength: 253 }}
                fullWidth
              />
            </IntegrationField>
          )}
          <IntegrationField label="Description" maxWidth={620}>
            <DnsTextField
              placeholder="Short purpose of this Provider Identity"
              value={form.description}
              onChange={event => setForm({ ...form, description: event.target.value })}
              fullWidth
            />
          </IntegrationField>
          {!isEdit ? (
            <Box>
              <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>
                Access token Secret source
              </Typography>
              <Box
                sx={{
                  columnGap: '16px',
                  display: 'grid',
                  gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' },
                  maxWidth: 820,
                  rowGap: '12px',
                }}
              >
                <ChoiceCard
                  selected={form.secretMode === 'create'}
                  title="Create Secret"
                  description="Create a same-namespace Kubernetes Secret with the Cloudflare API token before creating this Provider Identity."
                  onClick={() => setForm({ ...form, secretMode: 'create' })}
                />
                <ChoiceCard
                  selected={form.secretMode === 'existing'}
                  title="Use existing Secret"
                  description="Reference a Secret that already contains the Cloudflare API token."
                  onClick={() => setForm({ ...form, secretMode: 'existing', accessToken: '' })}
                />
              </Box>
            </Box>
          ) : null}
          <IntegrationField
            label={isEdit || form.secretMode === 'existing' ? 'Access token Secret' : 'Secret name'}
            error={secretNameError}
            help={
              form.secretMode === 'create' && !isEdit
                ? 'The Secret will be created in the same namespace as this Provider Identity.'
                : 'The Secret must be in the same namespace and contain a raw Cloudflare API token with Account Settings:Read for exactly one account, plus Zone:Edit and DNS:Edit for all zones from that account.'
            }
            maxWidth={620}
          >
            <DnsTextField
              placeholder="cloudflare-api-token"
              value={form.secretName}
              onChange={event => setForm({ ...form, secretName: event.target.value })}
              error={Boolean(secretNameError)}
              fullWidth
            />
          </IntegrationField>
          <IntegrationField label="Secret key" error={secretKeyError} maxWidth={620}>
            <DnsTextField
              placeholder="api-token"
              value={form.secretKey}
              onChange={event => setForm({ ...form, secretKey: event.target.value })}
              error={Boolean(secretKeyError)}
              fullWidth
            />
          </IntegrationField>
          {isEdit ? (
            <Box>
              <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Token update</Typography>
              <Box
                sx={{
                  columnGap: '16px',
                  display: 'grid',
                  gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' },
                  maxWidth: 820,
                  rowGap: '12px',
                }}
              >
                <ChoiceCard
                  selected={form.tokenUpdateMode === 'unchanged'}
                  title="Leave token unchanged"
                  description="Save Provider Identity settings without reading or updating the referenced Secret value."
                  onClick={() =>
                    setForm({ ...form, tokenUpdateMode: 'unchanged', accessToken: '' })
                  }
                />
                <ChoiceCard
                  selected={form.tokenUpdateMode === 'update'}
                  title="Update token"
                  description="Write a new Cloudflare API token to the selected Secret key during save."
                  onClick={() => setForm({ ...form, tokenUpdateMode: 'update' })}
                />
              </Box>
            </Box>
          ) : null}
          {(!isEdit && form.secretMode === 'create') ||
          (isEdit && form.tokenUpdateMode === 'update') ? (
            <IntegrationField
              label="Cloudflare API token"
              error={accessTokenError}
              help={
                isEdit
                  ? 'The existing token is not read or displayed. The new token is written to the selected Secret key when you save.'
                  : 'The token is saved to the Secret and is redacted from the manifest preview.'
              }
              maxWidth={620}
            >
              <DnsTextField
                placeholder="Paste Cloudflare API token"
                value={form.accessToken}
                onChange={event => setForm({ ...form, accessToken: event.target.value })}
                error={Boolean(accessTokenError)}
                type="password"
                fullWidth
              />
            </IntegrationField>
          ) : null}
        </Stack>
        <YamlPreview value={preview} complete={previewComplete} />
      </FormPanel>
      {snackbar}
    </Stack>
  );
}

export function IdentityFormPage({
  namespaces,
  identity,
  onBack,
  onSaved,
  breadcrumb,
}: {
  namespaces: string[];
  identity?: Route53Identity;
  onBack: () => void;
  onSaved: () => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const { showSuccess, showError, snackbar } = useNotice();
  const isEdit = Boolean(identity);
  const [showValidation, setShowValidation] = React.useState(false);
  const [credentialMode, setCredentialMode] = React.useState<'runtime' | 'assume'>(
    identity?.spec.assumeRoleChain?.length ? 'assume' : 'runtime'
  );
  const initialForm = React.useMemo<IdentityForm>(
    () =>
      identity
        ? identityFormFromResource(identity)
        : {
            namespace: '',
            name: '',
            description: '',
            accountID: '',
            region: '',
            assumeRoleChain: [],
          },
    [identity]
  );
  const [form, setForm] = useDnsFormState<IdentityForm>(initialForm);

  React.useEffect(() => {
    if (identity) {
      setCredentialMode(identity.spec.assumeRoleChain?.length ? 'assume' : 'runtime');
      setShowValidation(false);
    }
  }, [identity]);

  const effectiveForm = credentialMode === 'runtime' ? { ...form, assumeRoleChain: [] } : form;
  const validation = validateIdentityForm(effectiveForm, isEdit);
  const previewComplete = !hasValidationErrors(validation);
  const namespaceError = showValidation ? validation.namespace : undefined;
  const nameError = showValidation ? validation.name : undefined;
  const accountIDError = showValidation ? validation.accountID : undefined;
  const regionError = showValidation ? validation.region : undefined;
  const preview = identity
    ? updatedRoute53IdentityPreview(identity, effectiveForm)
    : route53IdentityPreview(effectiveForm);
  const updateRole = (index: number, patch: Partial<AssumeRoleRow>) => {
    setForm(current => ({
      ...current,
      assumeRoleChain: current.assumeRoleChain.map((role, roleIndex) =>
        roleIndex === index ? { ...role, ...patch } : role
      ),
    }));
  };

  async function save() {
    try {
      if (hasValidationErrors(validation)) {
        setShowValidation(true);
        showError('Fix validation errors before saving.');
        return;
      }
      if (identity) {
        await updateRoute53Identity(buildUpdatedRoute53IdentityManifest(identity, effectiveForm));
        showSuccess('Provider Identity updated');
      } else {
        const manifest = route53IdentityPreview(effectiveForm);
        if ('error' in manifest) {
          throw new Error(String(manifest.error));
        }
        await createRoute53Identity(manifest);
        showSuccess('Provider Identity created');
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
        title={isEdit ? 'Edit Provider Identity' : 'New Provider Identity'}
        description={
          isEdit
            ? `Update credential settings for ${form.namespace}/${form.name}.`
            : 'Create an Amazon Route 53 Provider Identity.'
        }
        action={
          <ToolbarButton label="Cancel" icon="mdi:close" tone="secondary" onClick={onBack} />
        }
      />
      <FormPanel
        title="Provider Identity settings"
        footer={
          <ToolbarButton
            label={isEdit ? 'Save changes' : 'Save Provider Identity'}
            icon="mdi:check"
            onClick={save}
          />
        }
      >
        <Stack spacing={2}>
          {isEdit ? (
            <Box sx={{ flex: 1 }}>
              <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Namespace</Typography>
              <FixedValue value={form.namespace} />
            </Box>
          ) : (
            <IntegrationField label="Namespace" error={namespaceError} maxWidth={620}>
              <DnsFormControl error={Boolean(namespaceError)} fullWidth>
                <Select
                  displayEmpty
                  value={form.namespace}
                  renderValue={value =>
                    value ? (
                      String(value)
                    ) : (
                      <Typography component="span" sx={{ color: ui.faint }}>
                        Select namespace
                      </Typography>
                    )
                  }
                  onChange={event => setForm({ ...form, namespace: String(event.target.value) })}
                >
                  <MenuItem disabled value="">
                    Select namespace
                  </MenuItem>
                  {namespaces.map(namespace => (
                    <MenuItem key={namespace} value={namespace}>
                      {namespace}
                    </MenuItem>
                  ))}
                </Select>
              </DnsFormControl>
            </IntegrationField>
          )}
          {isEdit ? (
            <Box sx={{ flex: 1 }}>
              <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Name</Typography>
              <FixedValue value={form.name} />
            </Box>
          ) : (
            <IntegrationField label="Name" error={nameError} maxWidth={620}>
              <DnsTextField
                placeholder="route53-identity"
                value={form.name}
                onChange={event => setForm({ ...form, name: event.target.value })}
                error={Boolean(nameError)}
                inputProps={{ maxLength: 253 }}
                fullWidth
              />
            </IntegrationField>
          )}
        </Stack>
        {isEdit ? (
          <Box>
            <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Description</Typography>
            <FixedValue value={form.description || '-'} />
          </Box>
        ) : (
          <IntegrationField label="Description" maxWidth={620}>
            <DnsTextField
              placeholder="Short purpose of this Provider Identity"
              value={form.description}
              onChange={event => setForm({ ...form, description: event.target.value })}
              fullWidth
            />
          </IntegrationField>
        )}
        {isEdit ? (
          <Box>
            <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>AWS account ID</Typography>
            <FixedValue value={form.accountID} />
          </Box>
        ) : (
          <IntegrationField
            label="AWS account ID"
            error={accountIDError}
            help="The controller checks this account before changing Route 53. It helps prevent DNS changes in the wrong AWS account."
            maxWidth={620}
          >
            <DnsTextField
              placeholder="123456789012"
              error={Boolean(accountIDError)}
              inputProps={{ inputMode: 'numeric', maxLength: 12, pattern: '[0-9]*' }}
              value={form.accountID}
              onChange={event => setForm({ ...form, accountID: event.target.value })}
              fullWidth
            />
          </IntegrationField>
        )}
        {isEdit ? (
          <Box>
            <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>AWS region</Typography>
            <FixedValue value={form.region || '-'} />
          </Box>
        ) : (
          <IntegrationField
            label="AWS region"
            error={regionError}
            help="Used for AWS SDK and STS endpoint resolution. Route 53 public hosted zones are global."
            maxWidth={620}
          >
            <DnsTextField
              placeholder="ap-northeast-1"
              error={Boolean(regionError)}
              value={form.region}
              onChange={event => setForm({ ...form, region: event.target.value })}
              fullWidth
            />
          </IntegrationField>
        )}
        <Box>
          <Typography sx={{ color: ui.muted, fontSize: 13, mb: 1 }}>Credential source</Typography>
          <Typography sx={{ color: ui.faint, fontSize: 13, mb: 1.5 }}>
            Choose how the controller gets AWS credentials before calling Route 53.
          </Typography>
          <Box
            sx={{ display: 'grid', gap: 1.5, gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' } }}
          >
            <ChoiceCard
              selected={credentialMode === 'runtime'}
              title="Runtime credentials"
              description="Use the credentials already available to the controller, such as IRSA or synced local development credentials."
              onClick={() => setCredentialMode('runtime')}
            />
            <ChoiceCard
              selected={credentialMode === 'assume'}
              title="Runtime + assume role chain"
              description="Start from runtime credentials, then assume the roles listed below before managing Route 53."
              onClick={() => setCredentialMode('assume')}
            />
          </Box>
        </Box>
        {credentialMode === 'assume' ? (
          <Box>
            <Stack
              direction="row"
              justifyContent="space-between"
              alignItems="center"
              sx={{ mb: 1.5 }}
            >
              <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                Assume role chain
              </Typography>
              <ToolbarButton
                label="Add role"
                icon="mdi:plus"
                tone="secondary"
                onClick={() =>
                  setForm(current => ({
                    ...current,
                    assumeRoleChain: [
                      ...current.assumeRoleChain,
                      { roleARN: '', sessionName: '', externalID: '' },
                    ],
                  }))
                }
              />
            </Stack>
            {form.assumeRoleChain.length ? (
              <Stack spacing={1.5}>
                {form.assumeRoleChain.map((role, index) => (
                  <Panel key={index} sx={{ p: 2 }}>
                    <Stack spacing={2}>
                      <Stack direction="row" justifyContent="space-between" alignItems="center">
                        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
                          Role {index + 1}
                        </Typography>
                        <ToolbarButton
                          label="Remove"
                          icon="mdi:delete"
                          tone="secondary"
                          onClick={() =>
                            setForm(current => ({
                              ...current,
                              assumeRoleChain: current.assumeRoleChain.filter(
                                (_, roleIndex) => roleIndex !== index
                              ),
                            }))
                          }
                        />
                      </Stack>
                      <IntegrationField label="Role ARN">
                        <DnsTextField
                          placeholder="arn:aws:iam::123456789012:role/dns-api-route53"
                          value={role.roleARN}
                          onChange={event => updateRole(index, { roleARN: event.target.value })}
                          fullWidth
                        />
                      </IntegrationField>
                      <Stack spacing={2}>
                        <IntegrationField
                          label="Session name"
                          error={showValidation ? validation.sessionNames[index] : undefined}
                        >
                          <DnsTextField
                            placeholder="dns-api-route53"
                            value={role.sessionName}
                            onChange={event =>
                              updateRole(index, { sessionName: event.target.value })
                            }
                            error={Boolean(showValidation && validation.sessionNames[index])}
                            inputProps={{ maxLength: 64 }}
                            fullWidth
                          />
                        </IntegrationField>
                        <IntegrationField label="External ID">
                          <DnsTextField
                            placeholder="optional"
                            value={role.externalID}
                            onChange={event =>
                              updateRole(index, { externalID: event.target.value })
                            }
                            fullWidth
                          />
                        </IntegrationField>
                      </Stack>
                    </Stack>
                  </Panel>
                ))}
              </Stack>
            ) : (
              <Alert severity="info">
                No role chain. The controller uses runtime credentials as-is.
              </Alert>
            )}
          </Box>
        ) : null}
        <YamlPreview value={preview} complete={previewComplete} />
      </FormPanel>
      {snackbar}
    </Stack>
  );
}
