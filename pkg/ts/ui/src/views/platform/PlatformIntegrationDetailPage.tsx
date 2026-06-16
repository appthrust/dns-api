/** @jsxRuntime classic */
import { Icon } from '../../components/Icon';
import { YamlCodeBlock } from '../../components/YamlCodeBlock';
import { Box, IconButton, Stack, Typography } from '../../components/primitives';
import React from 'react';
import { useDnsData } from '../../api/dns';
import { useDnsPlatform } from '../../platform';
import { nameOf, namespaceOf, resourceKey } from '../../resources';
import type { DnsCondition, KubeEvent, ProviderIdentity, ZoneClass } from '../../types/resources';
import {
  CopyFeedbackButton,
  DataGridTable,
  DesignLinkButton,
  descriptionOf,
  EmptyState,
  eventsForResource,
  GridCell,
  GridHeader,
  GridRow,
  Page,
  Panel,
  ProviderBadge,
  providerForResource,
  ResourceConditionsPanel,
  ResourceEventsPanel,
  resourceYamlPath,
  StatusBadge,
  tableHeaderSx,
  toYaml,
  ToolbarButton,
  ui,
  useNotice,
} from '../common/ui';
import {
  findIdentity,
  useIdentityAccess,
  useZoneClassAccess,
  zoneClassesReferencingIdentity,
} from './resources';
import {
  INTEGRATIONS_BREADCRUMB,
  integrationDeletePath,
  integrationEditPath,
  PlatformMissingResourcePage,
  usePlatformNavigation,
  useResourceRouteParams,
  zoneClassDetailPath,
  zoneClassNewPath,
} from './routes';

type IdentityTab = 'zoneClasses' | 'status' | 'credentials' | 'manifest';
type Health = 'Healthy' | 'Syncing' | 'Degraded';

const identityTabItems: Array<{ id: IdentityTab; label: string }> = [
  { id: 'zoneClasses', label: 'ZoneClasses' },
  { id: 'status', label: 'Status' },
  { id: 'credentials', label: 'Credentials' },
  { id: 'manifest', label: 'Manifest' },
];

export function PlatformIntegrationDetailPage() {
  const platform = useDnsPlatform();
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const params = useResourceRouteParams();
  const { showError, snackbar } = useNotice();
  const [activeTab, setActiveTab] = React.useState<IdentityTab>('zoneClasses');
  const {
    canUpdateCloudflareIdentity,
    canUpdateRoute53Identity,
    canDeleteCloudflareIdentity,
    canDeleteRoute53Identity,
  } = useIdentityAccess();
  const { canCreateZoneClass } = useZoneClassAccess();
  const selectedIdentity = findIdentity(data.identities.items, params.namespace, params.name);
  const liveYaml = React.useMemo(
    () => (selectedIdentity ? toYaml(selectedIdentity) : ''),
    [selectedIdentity],
  );

  if (!selectedIdentity) {
    return (
      <PlatformMissingResourcePage
        active="integrations"
        breadcrumb={[INTEGRATIONS_BREADCRUMB]}
        title="Provider Identity not found"
        body="The requested Provider Identity is not visible in this cluster."
      />
    );
  }

  const referencingZoneClasses = zoneClassesReferencingIdentity(
    data.zoneClasses.items,
    selectedIdentity,
  );
  const events = eventsForResource(data.events.items, selectedIdentity);
  const provider = providerForResource(selectedIdentity);
  const health = identityHealth(selectedIdentity);
  const canUpdateSelectedIdentity =
    selectedIdentity.kind === 'CloudflareIdentity'
      ? canUpdateCloudflareIdentity
      : canUpdateRoute53Identity;
  const canDeleteSelectedIdentity =
    selectedIdentity.kind === 'CloudflareIdentity'
      ? canDeleteCloudflareIdentity
      : canDeleteRoute53Identity;

  async function copyText(value: string) {
    try {
      await platform.clipboard.writeText(value);
    } catch (error) {
      showError(error);
      throw error;
    }
  }

  return (
    <Page
      breadcrumb={[INTEGRATIONS_BREADCRUMB]}
      title={nameOf(selectedIdentity)}
      description={
        descriptionOf(selectedIdentity) ||
        `${namespaceOf(selectedIdentity)}/${nameOf(selectedIdentity)}`
      }
      actions={
        <>
          <ToolbarButton
            label="Edit"
            icon="mdi:pencil"
            tone="secondary"
            disabled={!canUpdateSelectedIdentity}
            onClick={() => navigation.push(integrationEditPath(selectedIdentity))}
          />
          <IdentityActionMenu
            canDelete={canDeleteSelectedIdentity}
            onDelete={() => navigation.push(integrationDeletePath(selectedIdentity))}
          />
        </>
      }
    >
      <Stack spacing={2}>
        <IdentityVitalsStrip
          health={health}
          identity={selectedIdentity}
          provider={provider}
          usedBy={referencingZoneClasses.length}
          onShowStatus={() => setActiveTab('status')}
        />
        <IdentityTabs
          activeTab={activeTab}
          zoneClassCount={referencingZoneClasses.length}
          onChange={setActiveTab}
        />

        {activeTab === 'zoneClasses' ? (
          <ZoneClassesTab
            zoneClasses={referencingZoneClasses}
            canCreateZoneClass={canCreateZoneClass}
            onCreateZoneClass={() => navigation.push(zoneClassNewPath(selectedIdentity))}
            onOpenZoneClass={zoneClass => navigation.push(zoneClassDetailPath(zoneClass))}
          />
        ) : null}

        {activeTab === 'status' ? (
          <IdentityStatusTab
            identity={selectedIdentity}
            events={events}
            eventsError={data.events.error}
          />
        ) : null}

        {activeTab === 'credentials' ? (
          <IdentityCredentialsTab identity={selectedIdentity} />
        ) : null}

        {activeTab === 'manifest' ? (
          <IdentityManifestTab
            identity={selectedIdentity}
            liveYaml={liveYaml}
            onCopy={() => copyText(liveYaml)}
            onEdit={() => platform.liveYaml.open(selectedIdentity)}
          />
        ) : null}
      </Stack>
      {snackbar}
    </Page>
  );
}

function IdentityActionMenu({
  canDelete,
  onDelete,
}: {
  canDelete: boolean;
  onDelete: () => void;
}) {
  const [open, setOpen] = React.useState(false);
  const menuRef = React.useRef<HTMLDivElement | null>(null);

  React.useEffect(() => {
    if (!open) {
      return undefined;
    }

    function closeOnOutside(event: PointerEvent) {
      if (menuRef.current?.contains(event.target as Node)) {
        return;
      }
      setOpen(false);
    }

    function closeOnEscape(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    }

    document.addEventListener('pointerdown', closeOnOutside);
    document.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('pointerdown', closeOnOutside);
      document.removeEventListener('keydown', closeOnEscape);
    };
  }, [open]);

  return (
    <div ref={menuRef} style={{ position: 'relative' }}>
      <IconButton
        aria-label="Resource actions"
        onClick={() => setOpen(current => !current)}
        sx={{ borderColor: ui.border, color: ui.text }}
      >
        <Icon icon="mdi:dots-horizontal" />
      </IconButton>
      {open ? (
        <Box
          sx={{
            bgcolor: ui.panelBg,
            border: 1,
            borderColor: ui.border,
            borderRadius: 1,
            boxShadow: '0 12px 32px rgba(15, 23, 42, 0.16)',
            minWidth: 260,
            p: 0.75,
            position: 'absolute',
            right: 0,
            top: 38,
            zIndex: 20,
          }}
        >
          <MenuButton
            danger
            disabled={!canDelete}
            onClick={() => {
              setOpen(false);
              onDelete();
            }}
          >
            Delete Provider Identity
          </MenuButton>
        </Box>
      ) : null}
    </div>
  );
}

function MenuButton({
  children,
  danger,
  disabled,
  onClick,
}: {
  children: React.ReactNode;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      disabled={disabled}
      onClick={onClick}
      style={{
        background: 'transparent',
        border: 0,
        borderRadius: 'var(--dns-ui-radius-sm, 4px)',
        color: danger ? 'var(--dns-ui-danger, #b42318)' : 'var(--dns-ui-text, #111827)',
        cursor: disabled ? 'not-allowed' : 'pointer',
        display: 'block',
        font: 'inherit',
        opacity: disabled ? 0.5 : 1,
        padding: '8px 10px',
        textAlign: 'left',
        whiteSpace: 'nowrap',
        width: '100%',
      }}
    >
      {children}
    </button>
  );
}

function IdentityVitalsStrip({
  health,
  identity,
  provider,
  usedBy,
  onShowStatus,
}: {
  health: Health;
  identity: ProviderIdentity;
  provider: string;
  usedBy: number;
  onShowStatus: () => void;
}) {
  const account = accountSummary(identity);
  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Box
        sx={{
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', md: '1fr 1.35fr 2.8fr' },
        }}
      >
        <VitalsColumn>
          <VitalsGroup label="Status">
            <Stack spacing={0.75} alignItems="flex-start">
              <HealthInline health={health} prominent />
              <DesignLinkButton onClick={onShowStatus}>
                Details <Icon icon="mdi:arrow-right" width={13} />
              </DesignLinkButton>
            </Stack>
          </VitalsGroup>
          <VitalsGroup label="Used by">
            <Stack direction="row" spacing={1} alignItems="baseline">
              <Typography
                sx={{
                  color: ui.text,
                  fontSize: 20,
                  fontWeight: 700,
                  lineHeight: 1,
                }}
              >
                {usedBy}
              </Typography>
              <Typography sx={{ color: ui.faint, fontSize: 13 }}>
                {zoneClassCountLabel(usedBy)}
              </Typography>
            </Stack>
          </VitalsGroup>
        </VitalsColumn>
        <VitalsColumn>
          <VitalsGroup label="Namespace">
            <VitalsValue>{namespaceOf(identity)}</VitalsValue>
          </VitalsGroup>
          <VitalsGroup label="Name">
            <VitalsValue>{nameOf(identity)}</VitalsValue>
          </VitalsGroup>
        </VitalsColumn>
        <VitalsColumn last>
          <VitalsGroup label="Provider">
            <Stack direction="row" alignItems="center" spacing={1.25} useFlexGap flexWrap="wrap">
              <ProviderBadge provider={provider} />
            </Stack>
            <Typography
              sx={{
                color: ui.faint,
                fontFamily: 'monospace',
                fontSize: 13,
                mt: 0.75,
              }}
            >
              Kind: {identity.kind}
            </Typography>
          </VitalsGroup>
          <VitalsGroup label={account.label}>
            <Typography
              sx={{
                color: account.value === '-' ? ui.faint : ui.text,
                fontFamily: 'monospace',
                fontSize: 13,
                lineHeight: 1.45,
                wordBreak: 'break-word',
              }}
            >
              {account.value}
            </Typography>
          </VitalsGroup>
        </VitalsColumn>
      </Box>
    </Panel>
  );
}

function IdentityTabs({
  activeTab,
  zoneClassCount,
  onChange,
}: {
  activeTab: IdentityTab;
  zoneClassCount: number;
  onChange: (tab: IdentityTab) => void;
}) {
  return (
    <Box sx={{ borderBottom: 1, borderColor: ui.border, overflowX: 'auto' }}>
      <Stack direction="row" spacing={1.5} useFlexGap flexWrap="nowrap">
        {identityTabItems.map(item => {
          const selected = item.id === activeTab;
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => onChange(item.id)}
              style={{
                alignItems: 'center',
                background: 'transparent',
                border: 0,
                borderBottom: selected
                  ? '2px solid var(--dns-ui-accent, #2563eb)'
                  : '2px solid transparent',
                color: selected
                  ? 'var(--dns-ui-accent, #2563eb)'
                  : 'var(--dns-ui-text-muted, #667085)',
                cursor: 'pointer',
                display: 'inline-flex',
                font: 'inherit',
                fontSize: 13,
                fontWeight: selected ? 700 : 600,
                gap: 6,
                minHeight: 42,
                padding: '0 8px',
                whiteSpace: 'nowrap',
              }}
            >
              {item.label}
              {item.id === 'zoneClasses' ? (
                <SmallBadge selected={selected}>{zoneClassCount}</SmallBadge>
              ) : null}
            </button>
          );
        })}
      </Stack>
    </Box>
  );
}

function IdentityStatusTab({
  identity,
  events,
  eventsError,
}: {
  identity: ProviderIdentity;
  events: KubeEvent[];
  eventsError: unknown;
}) {
  return (
    <Stack spacing={1.5}>
      <ResourceConditionsPanel conditions={identity.status?.conditions ?? []} />
      <ResourceEventsPanel events={events} error={eventsError} />
    </Stack>
  );
}

function IdentityCredentialsTab({ identity }: { identity: ProviderIdentity }) {
  const checkFields: Array<[string, React.ReactNode]> = [
    [
      'Last credential check',
      timeWithRelative(identity.status?.lastCredentialCheckTime, 'Not checked'),
    ],
    [
      'Next credential check',
      timeWithRelative(identity.status?.nextCredentialCheckTime, 'Not scheduled'),
    ],
  ];

  if (identity.kind === 'CloudflareIdentity') {
    return (
      <Panel sx={{ overflow: 'hidden' }}>
        <Box
          sx={{
            display: 'grid',
            gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' },
          }}
        >
          <Box sx={{ borderRight: { md: 1 }, borderColor: ui.borderSoft, p: 2 }}>
            <DetailSection
              title="Cloudflare account"
              fields={[
                ['Account ID', identity.status?.account?.id ?? '-'],
                ['Account name', identity.status?.account?.name ?? '-'],
                ['Account type', identity.status?.account?.type ?? '-'],
              ]}
            />
          </Box>
          <Box sx={{ p: 2 }}>
            <DetailSection
              title="Access token"
              fields={[
                ['Secret', identity.spec.accessToken.secretRef.name],
                ['Secret key', identity.spec.accessToken.secretRef.key],
                ['Status', identity.status?.accessToken?.status ?? '-'],
                ['Token ID', identity.status?.accessToken?.id ?? '-'],
                ['Not before', identity.status?.accessToken?.notBefore ?? '-'],
                ['Expires on', identity.status?.accessToken?.expiresOn ?? '-'],
                ...checkFields,
              ]}
            />
          </Box>
        </Box>
      </Panel>
    );
  }

  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Box
        sx={{
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' },
        }}
      >
        <Box sx={{ borderRight: { md: 1 }, borderColor: ui.borderSoft, p: 2 }}>
          <DetailSection
            title="AWS account"
            fields={[
              ['Account ID', identity.spec.accountID],
              ['Region', identity.spec.region],
            ]}
          />
        </Box>
        <Box sx={{ p: 2 }}>
          <DetailSection
            title="Runtime credentials"
            fields={[
              ['Credential source', identity.spec.credentials.runtime ? 'Runtime' : '-'],
              ...checkFields,
            ]}
          />
        </Box>
      </Box>
      {identity.spec.assumeRoleChain?.length ? (
        <Box sx={{ borderTop: 1, borderColor: ui.borderSoft, px: 2, py: 1.75 }}>
          <Stack direction="row" spacing={1} alignItems="baseline" sx={{ mb: 0.75 }}>
            <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
              Assume role chain
            </Typography>
            <Typography sx={{ color: ui.faint, fontSize: 13 }}>
              {identity.spec.assumeRoleChain.length} role
              {identity.spec.assumeRoleChain.length === 1 ? '' : 's'}
            </Typography>
          </Stack>
          <Stack spacing={0}>
            {identity.spec.assumeRoleChain.map((role, index) => (
              <Box
                key={`${role.roleARN}-${index}`}
                sx={{
                  borderBottom: index === identity.spec.assumeRoleChain!.length - 1 ? 0 : 1,
                  borderColor: ui.borderSoft,
                  py: 1.25,
                }}
              >
                <Typography sx={tableHeaderSx}>Role {index + 1}</Typography>
                <Typography
                  sx={{
                    color: ui.text,
                    fontSize: 14,
                    fontWeight: 400,
                    lineHeight: 1.55,
                    mt: 0.75,
                    wordBreak: 'break-word',
                  }}
                >
                  {role.roleARN}
                </Typography>
                {role.externalID || role.sessionName ? (
                  <Typography sx={{ color: ui.faint, fontSize: 13, mt: 0.75 }}>
                    {[
                      role.externalID ? `externalID: ${role.externalID}` : '',
                      role.sessionName ? `sessionName: ${role.sessionName}` : '',
                    ]
                      .filter(Boolean)
                      .join(' · ')}
                  </Typography>
                ) : null}
              </Box>
            ))}
          </Stack>
        </Box>
      ) : null}
    </Panel>
  );
}

function IdentityManifestTab({
  identity,
  liveYaml,
  onCopy,
  onEdit,
}: {
  identity: ProviderIdentity;
  liveYaml: string;
  onCopy: () => void;
  onEdit: () => void;
}) {
  const editPath = resourceYamlPath(identity);
  return (
    <Panel sx={{ p: 2.5 }}>
      <Stack
        direction="row"
        justifyContent="space-between"
        alignItems="center"
        spacing={1.5}
        sx={{ mb: 1.5 }}
      >
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>Manifest</Typography>
        <Stack direction="row" spacing={0.5}>
          <CopyFeedbackButton onCopy={onCopy} />
          {editPath ? (
            <ToolbarButton label="Edit Manifest" icon="mdi:file-code-outline" onClick={onEdit} />
          ) : null}
        </Stack>
      </Stack>
      <YamlCodeBlock code={liveYaml} maxHeight="none" />
    </Panel>
  );
}

function ZoneClassesTab({
  zoneClasses,
  canCreateZoneClass,
  onCreateZoneClass,
  onOpenZoneClass,
}: {
  zoneClasses: ZoneClass[];
  canCreateZoneClass: boolean;
  onCreateZoneClass: () => void;
  onOpenZoneClass: (zoneClass: ZoneClass) => void;
}) {
  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Stack
        direction={{ xs: 'column', md: 'row' }}
        alignItems={{ xs: 'stretch', md: 'center' }}
        justifyContent="flex-end"
        spacing={1.5}
        sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}
      >
        <Stack direction="row" alignItems="center" justifyContent="flex-end" spacing={1}>
          <Typography sx={{ color: ui.faint, fontSize: 13 }}>
            {zoneClasses.length} of {zoneClasses.length}
          </Typography>
          <ToolbarButton
            label="Add ZoneClass"
            icon="mdi:plus"
            disabled={!canCreateZoneClass}
            onClick={onCreateZoneClass}
          />
        </Stack>
      </Stack>
      {zoneClasses.length ? (
        <DataGridTable
          columns="minmax(220px,1.1fr) minmax(150px,0.75fr) minmax(150px,0.75fr) minmax(260px,1.35fr) minmax(130px,0.55fr)"
          framed={false}
        >
          <GridHeader
            labels={[
              { label: 'Name' },
              { label: 'Zone creation' },
              { label: 'Zone deletion' },
              { label: 'Description' },
              { label: 'Status' },
            ]}
          />
          {zoneClasses.map(zoneClass => (
            <GridRow key={resourceKey(zoneClass)}>
              <GridCell>
                <Stack spacing={0.25} sx={{ minWidth: 0 }}>
                  <DesignLinkButton onClick={() => onOpenZoneClass(zoneClass)}>
                    {nameOf(zoneClass)}
                  </DesignLinkButton>
                  <Typography sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 12 }}>
                    {namespaceOf(zoneClass)}/{nameOf(zoneClass)}
                  </Typography>
                </Stack>
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.text, fontSize: 14 }}>
                  {zoneClass.spec.parameters.zoneCreationPolicy ?? 'Create'}
                </Typography>
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.text, fontSize: 14 }}>
                  {zoneClass.spec.parameters.zoneDeletionPolicy ?? 'Retain'}
                </Typography>
              </GridCell>
              <GridCell>
                <Typography
                  sx={{
                    color: descriptionOf(zoneClass) ? ui.text : ui.faint,
                    fontSize: 14,
                    lineHeight: 1.5,
                  }}
                >
                  {descriptionOf(zoneClass) || 'No description'}
                </Typography>
              </GridCell>
              <GridCell>
                <HealthBadge health={zoneClassHealth(zoneClass)} />
              </GridCell>
            </GridRow>
          ))}
        </DataGridTable>
      ) : (
        <EmptyState
          title="No ZoneClasses"
          body="No ZoneClass references this identity."
          action={
            <ToolbarButton
              label="Add ZoneClass"
              icon="mdi:plus"
              disabled={!canCreateZoneClass}
              onClick={onCreateZoneClass}
            />
          }
        />
      )}
    </Panel>
  );
}

function SmallBadge({
  children,
  selected,
}: {
  children: React.ReactNode;
  selected?: boolean;
}) {
  return (
    <span
      style={{
        alignItems: 'center',
        background: selected
          ? 'var(--dns-ui-accent, #2563eb)'
          : 'var(--dns-ui-surface-muted, #f6f8fb)',
        border: selected ? 0 : '1px solid var(--dns-ui-border, #d7dde8)',
        borderRadius: 999,
        color: selected ? 'var(--dns-ui-on-accent, #ffffff)' : 'var(--dns-ui-text-muted, #667085)',
        display: 'inline-flex',
        fontSize: 11,
        fontWeight: 700,
        height: 20,
        justifyContent: 'center',
        lineHeight: 1,
        minWidth: 20,
        padding: '0 6px',
      }}
    >
      {children}
    </span>
  );
}

function DetailSection({
  title,
  fields,
}: {
  title: string;
  fields: Array<[string, React.ReactNode]>;
}) {
  return (
    <Box>
      <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>{title}</Typography>
      <Stack spacing={1.5} sx={{ mt: 1.5 }}>
        {fields.map(([label, value]) => (
          <Box key={label}>
            <Typography sx={tableHeaderSx}>{label}</Typography>
            <Typography
              sx={{
                color: value === '-' ? ui.faint : ui.text,
                fontSize: 14,
                fontWeight: 400,
                lineHeight: 1.55,
                mt: 0.65,
                wordBreak: 'break-word',
              }}
            >
              {value || '-'}
            </Typography>
          </Box>
        ))}
      </Stack>
    </Box>
  );
}

function VitalsColumn({ children, last }: { children: React.ReactNode; last?: boolean }) {
  return (
    <Box
      sx={{
        borderRight: last ? 0 : 1,
        borderColor: ui.borderSoft,
        minHeight: 170,
        p: 2.5,
      }}
    >
      <Stack spacing={3}>{children}</Stack>
    </Box>
  );
}

function VitalsGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Box>
      <Typography sx={tableHeaderSx}>{label}</Typography>
      <Box sx={{ mt: 1 }}>{children}</Box>
    </Box>
  );
}

function VitalsValue({ children }: { children: React.ReactNode }) {
  return (
    <Typography sx={{ color: ui.text, fontSize: 16, fontWeight: 700, lineHeight: 1.35 }}>
      {children}
    </Typography>
  );
}

function HealthInline({ health, prominent }: { health: Health; prominent?: boolean }) {
  const tone = health === 'Healthy' ? 'success' : health === 'Syncing' ? 'warning' : 'danger';
  return (
    <Stack direction="row" spacing={0.75} alignItems="center">
      <Box sx={{ bgcolor: tone, borderRadius: 999, height: 8, width: 8 }} />
      <Typography
        sx={{
          color:
            health === 'Healthy'
              ? 'var(--dns-ui-success, #138a5b)'
              : health === 'Syncing'
                ? ui.warningText
                : ui.dangerText,
          fontSize: prominent ? 16 : 13,
          fontWeight: 700,
          lineHeight: 1.2,
        }}
      >
        {health}
      </Typography>
    </Stack>
  );
}

function HealthBadge({ health }: { health: Health }) {
  return (
    <StatusBadge
      tone={health === 'Healthy' ? 'success' : health === 'Syncing' ? 'pending' : 'danger'}
      label={health}
    />
  );
}

function identityHealth(identity: ProviderIdentity): Health {
  return resourceConditionsHealth(identity.status?.conditions, ['Accepted', 'Ready']);
}

function zoneClassHealth(zoneClass: ZoneClass): Health {
  return resourceConditionsHealth(zoneClass.status?.conditions, ['Accepted']);
}

function resourceConditionsHealth(
  conditions: DnsCondition[] | undefined,
  expectedTypes: string[],
): Health {
  const expected = expectedTypes.map(type =>
    conditions?.find(condition => condition.type === type),
  );
  if (!conditions?.length || expected.some(condition => !condition)) {
    return 'Syncing';
  }
  if (expected.some(condition => condition?.status === 'False')) {
    return 'Degraded';
  }
  if (expected.every(condition => condition?.status === 'True')) {
    return 'Healthy';
  }
  return 'Syncing';
}

function accountSummary(identity: ProviderIdentity) {
  if (identity.kind === 'CloudflareIdentity') {
    return {
      label: 'Cloudflare account ID',
      value: identity.status?.account?.id ?? '-',
    };
  }
  return {
    label: 'AWS account ID',
    value: identity.spec.accountID,
  };
}

function zoneClassCountLabel(count: number) {
  return `ZoneClass${count === 1 ? '' : 'es'}`;
}

function timeWithRelative(value: string | undefined, fallback: string) {
  if (!value) {
    return fallback;
  }
  const relative = relativeTime(value);
  return relative ? `${value} · ${relative}` : value;
}

function relativeTime(value?: string) {
  if (!value) {
    return '';
  }
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) {
    return '';
  }
  const diffSeconds = Math.round((Date.now() - timestamp) / 1000);
  const future = diffSeconds < 0;
  const absoluteSeconds = Math.abs(diffSeconds);
  const suffix = future ? '' : ' ago';
  const prefix = future ? 'in ' : '';
  if (absoluteSeconds < 60) {
    return `${prefix}${absoluteSeconds} sec${suffix}`;
  }
  const minutes = Math.round(absoluteSeconds / 60);
  if (minutes < 60) {
    return `${prefix}${minutes} min${suffix}`;
  }
  const hours = Math.round(minutes / 60);
  if (hours < 24) {
    return `${prefix}${hours} hr${suffix}`;
  }
  const days = Math.round(hours / 24);
  return `${prefix}${days} d${suffix}`;
}
