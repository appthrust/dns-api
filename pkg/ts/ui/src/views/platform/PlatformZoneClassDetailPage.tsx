/** @jsxRuntime classic */
import { Icon } from '../../components/Icon';
import { YamlCodeBlock } from '../../components/YamlCodeBlock';
import { Box, IconButton, Stack, Typography } from '../../components/primitives';
import React from 'react';
import { useAccess, useDnsData } from '../../api/dns';
import { useDnsPlatform } from '../../platform';
import { nameOf, namespaceOf, resourceKey, ZoneResource, zoneClassIdentityName } from '../../resources';
import type { DnsCondition, KubeEvent, ProviderIdentity, Zone, ZoneClass } from '../../types/resources';
import {
  CopyFeedbackButton,
  descriptionOf,
  DesignLinkButton,
  EmptyState,
  eventsForResource,
  namespaceLabelsYaml,
  Page,
  Panel,
  ProviderBadge,
  providerForResource,
  ResourceConditionsPanel,
  ResourceEventsPanel,
  resourceYamlPath,
  tableHeaderSx,
  toYaml,
  ToolbarButton,
  ui,
  useNotice,
} from '../common/ui';
import { newZonePath, zonePath } from '../zones/routes';
import { ZonesTable } from '../zones/ZonesPage';
import {
  findIdentity,
  findZoneClass,
  useZoneClassAccess,
  zoneClassPolicyFields,
  zonesReferencingZoneClass,
} from './resources';
import {
  integrationDetailPath,
  PlatformMissingResourcePage,
  usePlatformNavigation,
  useResourceRouteParams,
  ZONE_CLASSES_BREADCRUMB,
  zoneClassDeletePath,
  zoneClassEditPath,
} from './routes';

type ZoneClassTab = 'zones' | 'status' | 'policy' | 'manifest';
type Health = 'Healthy' | 'Syncing' | 'Degraded';

const tabItems: Array<{ id: ZoneClassTab; label: string }> = [
  { id: 'zones', label: 'Zones' },
  { id: 'status', label: 'Status' },
  { id: 'policy', label: 'Policy' },
  { id: 'manifest', label: 'Manifest' },
];

export function PlatformZoneClassDetailPage() {
  const platform = useDnsPlatform();
  const navigation = usePlatformNavigation();
  const data = useDnsData();
  const params = useResourceRouteParams();
  const { showError, snackbar } = useNotice();
  const [activeTab, setActiveTab] = React.useState<ZoneClassTab>('zones');
  const { canUpdateZoneClass, canDeleteZoneClass } = useZoneClassAccess();
  const canCreateZone = useAccess(ZoneResource, 'create', {
    group: 'dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'zones',
  });
  const selectedZoneClass = findZoneClass(data.zoneClasses.items, params.namespace, params.name);
  const liveYaml = React.useMemo(
    () => (selectedZoneClass ? toYaml(selectedZoneClass) : ''),
    [selectedZoneClass],
  );

  if (!selectedZoneClass) {
    return (
      <PlatformMissingResourcePage
        active="zoneclasses"
        breadcrumb={[ZONE_CLASSES_BREADCRUMB]}
        title="ZoneClass not found"
        body="The requested ZoneClass is not visible in this cluster."
      />
    );
  }

  const identityName = zoneClassIdentityName(selectedZoneClass);
  const identity = identityName
    ? findIdentity(data.identities.items, namespaceOf(selectedZoneClass), identityName)
    : undefined;
  const referencingZones = zonesReferencingZoneClass(data.zones.items, selectedZoneClass);
  const events = eventsForResource(data.events.items, selectedZoneClass);
  const provider = providerForResource(selectedZoneClass);
  const health = resourceConditionsHealth(selectedZoneClass.status?.conditions, ['Accepted']);

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
      breadcrumb={[ZONE_CLASSES_BREADCRUMB]}
      title={nameOf(selectedZoneClass)}
      description={
        descriptionOf(selectedZoneClass) ||
        `${namespaceOf(selectedZoneClass)}/${nameOf(selectedZoneClass)}`
      }
      actions={
        <>
          <ToolbarButton
            label="Edit"
            icon="mdi:pencil"
            tone="secondary"
            disabled={!canUpdateZoneClass}
            onClick={() => navigation.push(zoneClassEditPath(selectedZoneClass))}
          />
          <ZoneClassActionMenu
            canDelete={canDeleteZoneClass}
            onDelete={() => navigation.push(zoneClassDeletePath(selectedZoneClass))}
          />
        </>
      }
    >
      <Stack spacing={2}>
        <ZoneClassVitalsStrip
          health={health}
          identity={identity}
          identityName={identityName}
          provider={provider}
          usedBy={referencingZones.length}
          zoneClass={selectedZoneClass}
          onOpenIdentity={
            identity ? () => navigation.push(integrationDetailPath(identity)) : undefined
          }
          onShowStatus={() => setActiveTab('status')}
        />
        <ZoneClassTabs
          activeTab={activeTab}
          zoneCount={referencingZones.length}
          onChange={setActiveTab}
        />

        {activeTab === 'zones' ? (
          <ZonesTab
            canCreateZone={canCreateZone}
            recordSets={data.recordSets.items}
            zones={referencingZones}
            onCreateZone={() => navigation.push(newZonePath(selectedZoneClass))}
            onOpenZone={zone => navigation.push(zonePath(zone))}
          />
        ) : null}

        {activeTab === 'status' ? (
          <StatusTab
            zoneClass={selectedZoneClass}
            identity={identity}
            identityName={identityName}
            events={events}
            eventsError={data.events.error}
            onOpenIdentity={
              identity ? () => navigation.push(integrationDetailPath(identity)) : undefined
            }
          />
        ) : null}

        {activeTab === 'policy' ? (
          <PolicyTab zoneClass={selectedZoneClass} />
        ) : null}

        {activeTab === 'manifest' ? (
          <ManifestTab
            zoneClass={selectedZoneClass}
            liveYaml={liveYaml}
            onCopy={() => copyText(liveYaml)}
            onEdit={() => platform.liveYaml.open(selectedZoneClass)}
          />
        ) : null}
      </Stack>
      {snackbar}
    </Page>
  );
}

function ZoneClassActionMenu({
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
            minWidth: 220,
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
            Delete ZoneClass
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

function ZoneClassVitalsStrip({
  health,
  identity,
  identityName,
  provider,
  usedBy,
  zoneClass,
  onOpenIdentity,
  onShowStatus,
}: {
  health: Health;
  identity?: ProviderIdentity;
  identityName?: string;
  provider: string;
  usedBy: number;
  zoneClass: ZoneClass;
  onOpenIdentity?: () => void;
  onShowStatus: () => void;
}) {
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
              <Typography sx={{ color: ui.text, fontSize: 20, fontWeight: 700, lineHeight: 1 }}>
                {usedBy}
              </Typography>
              <Typography sx={{ color: ui.faint, fontSize: 13 }}>
                Zone{usedBy === 1 ? '' : 's'}
              </Typography>
            </Stack>
          </VitalsGroup>
        </VitalsColumn>
        <VitalsColumn>
          <VitalsGroup label="Namespace">
            <VitalsValue>{namespaceOf(zoneClass)}</VitalsValue>
          </VitalsGroup>
          <VitalsGroup label="Name">
            <VitalsValue>{nameOf(zoneClass)}</VitalsValue>
          </VitalsGroup>
        </VitalsColumn>
        <VitalsColumn last>
          <VitalsGroup label="Provider">
            <Stack direction="row" alignItems="center" spacing={1.25} useFlexGap flexWrap="wrap">
              <ProviderBadge provider={provider} />
            </Stack>
            <Typography sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 13, mt: 0.75 }}>
              {`${zoneClass.spec.provider.name}/${zoneClass.spec.provider.version}`}
            </Typography>
          </VitalsGroup>
          <VitalsGroup label="Provider identity">
            {identity ? (
              <DesignLinkButton onClick={onOpenIdentity}>
                {resourceKey(identity)}
              </DesignLinkButton>
            ) : (
              <Typography
                sx={{
                  color: ui.faint,
                  fontFamily: 'monospace',
                  fontSize: 13,
                  lineHeight: 1.45,
                  wordBreak: 'break-word',
                }}
              >
                {identityName ? `${namespaceOf(zoneClass)}/${identityName}` : 'No identityRef'}
              </Typography>
            )}
          </VitalsGroup>
        </VitalsColumn>
      </Box>
    </Panel>
  );
}

function ZoneClassTabs({
  activeTab,
  zoneCount,
  onChange,
}: {
  activeTab: ZoneClassTab;
  zoneCount: number;
  onChange: (tab: ZoneClassTab) => void;
}) {
  return (
    <Box sx={{ borderBottom: 1, borderColor: ui.border, overflowX: 'auto' }}>
      <Stack direction="row" spacing={1.5} useFlexGap flexWrap="nowrap">
        {tabItems.map(item => {
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
              {item.id === 'zones' ? <SmallBadge selected={selected}>{zoneCount}</SmallBadge> : null}
            </button>
          );
        })}
      </Stack>
    </Box>
  );
}

function ZonesTab({
  canCreateZone,
  recordSets,
  zones,
  onCreateZone,
  onOpenZone,
}: {
  canCreateZone: boolean;
  recordSets: Parameters<typeof ZonesTable>[0]['recordSets'];
  zones: Zone[];
  onCreateZone: () => void;
  onOpenZone: (zone: Zone) => void;
}) {
  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Stack
        direction="row"
        alignItems="center"
        justifyContent="flex-end"
        spacing={1}
        sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}
      >
        <Typography sx={{ color: ui.faint, fontSize: 13 }}>
          {zones.length} of {zones.length}
        </Typography>
        <ToolbarButton
          label="Add Zone"
          icon="mdi:plus"
          disabled={!canCreateZone}
          onClick={onCreateZone}
        />
      </Stack>
      {zones.length ? (
        <ZonesTable
          framed={false}
          recordSets={recordSets}
          zones={zones}
          onOpenZone={onOpenZone}
        />
      ) : (
        <EmptyState
          title="No Zones"
          body="No Zone references this ZoneClass."
          action={
            <ToolbarButton
              label="Add Zone"
              icon="mdi:plus"
              disabled={!canCreateZone}
              onClick={onCreateZone}
            />
          }
        />
      )}
    </Panel>
  );
}

function StatusTab({
  zoneClass,
  identity,
  identityName,
  events,
  eventsError,
  onOpenIdentity,
}: {
  zoneClass: ZoneClass;
  identity?: ProviderIdentity;
  identityName?: string;
  events: KubeEvent[];
  eventsError: unknown;
  onOpenIdentity?: () => void;
}) {
  const zoneClassHealth = resourceConditionsHealth(zoneClass.status?.conditions, ['Accepted']);
  const identityHealth = resourceConditionsHealth(identity?.status?.conditions, ['Accepted', 'Ready']);
  return (
    <Stack spacing={1.5}>
      <Panel sx={{ p: 2.25 }}>
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 700, mb: 1.5 }}>
          Health breakdown
        </Typography>
        <Stack spacing={0}>
          <HealthRow
            label="ZoneClass"
            health={zoneClassHealth}
            reason={conditionSummary(zoneClass.status?.conditions)}
            divided
          />
          <HealthRow
            label="Provider identity"
            health={identityHealth}
            reason={
              identity
                ? conditionSummary(identity.status?.conditions)
                : identityName
                  ? `${namespaceOf(zoneClass)}/${identityName} is not visible`
                  : 'No identityRef'
            }
            action={
              identity ? (
                <DesignLinkButton onClick={onOpenIdentity}>
                  View <Icon icon="mdi:arrow-right" width={14} />
                </DesignLinkButton>
              ) : undefined
            }
          />
        </Stack>
      </Panel>
      <ResourceConditionsPanel conditions={zoneClass.status?.conditions ?? []} />
      <ResourceEventsPanel events={events} error={eventsError} />
    </Stack>
  );
}

function PolicyTab({ zoneClass }: { zoneClass: ZoneClass }) {
  const platform = useDnsPlatform();
  const allowedLabelsYaml = namespaceLabelsYaml(
    zoneClass.spec.allowedZones.namespaces.selector?.matchLabels,
  );
  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Box
        sx={{
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' },
        }}
      >
        <Box sx={{ borderRight: { md: 1 }, borderColor: ui.borderSoft, p: 2.5 }}>
          <ProviderPolicySection zoneClass={zoneClass} />
        </Box>
        <Box sx={{ p: 2.5 }}>
          <Stack direction="row" alignItems="center" justifyContent="space-between" spacing={1}>
            <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
              Allowed zone namespaces
            </Typography>
            <CopyFeedbackButton
              size="compact"
              disabled={!allowedLabelsYaml.trim()}
              onCopy={() => platform.clipboard.writeText(allowedLabelsYaml)}
            />
          </Stack>
          <Typography sx={{ color: ui.faint, fontSize: 14, lineHeight: 1.6, mt: 1 }}>
            Namespaces with these labels can create Zones from this ZoneClass.
          </Typography>
          <Box sx={{ mt: 1.5 }}>
            <YamlCodeBlock
              code={allowedLabelsYaml}
              emptyText="No namespace labels are required."
              maxHeight="none"
            />
          </Box>
        </Box>
      </Box>
      <Box sx={{ borderTop: 1, borderColor: ui.borderSoft, p: 2.5 }}>
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1.5 }}>
          Provider parameters
        </Typography>
        <YamlCodeBlock code={toYaml(zoneClass.spec.parameters)} maxHeight="none" />
      </Box>
    </Panel>
  );
}

function ProviderPolicySection({ zoneClass }: { zoneClass: ZoneClass }) {
  return (
    <Box>
      <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>Provider policy</Typography>
      <Stack spacing={1.75} sx={{ mt: 1.5 }}>
        <PolicyDetail label="Provider" value={`${zoneClass.spec.provider.name}/${zoneClass.spec.provider.version}`} />
        {zoneClassPolicyFields(zoneClass).map(([label, value]) => (
          <PolicyDetail
            key={label}
            label={label}
            value={value}
            description={zoneClassPolicyDescription(label, String(value ?? ''))}
          />
        ))}
      </Stack>
    </Box>
  );
}

function PolicyDetail({
  label,
  value,
  description,
}: {
  label: string;
  value: React.ReactNode;
  description?: string;
}) {
  return (
    <Box>
      <Typography sx={tableHeaderSx}>{label}</Typography>
      <Typography
        sx={{
          color: value === '-' ? ui.faint : ui.text,
          fontSize: 14,
          fontWeight: 600,
          lineHeight: 1.55,
          mt: 0.65,
          wordBreak: 'break-word',
        }}
      >
        {value || '-'}
      </Typography>
      {description ? (
        <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.55, mt: 0.35 }}>
          {description}
        </Typography>
      ) : null}
    </Box>
  );
}

function zoneClassPolicyDescription(label: string, value: string): string {
  if (label === 'Zone creation policy') {
    return value === 'Deny'
      ? 'New Zones may reference this ZoneClass, but the controller does not create provider zones.'
      : 'The controller creates a provider zone when a Zone uses this ZoneClass.';
  }
  if (label === 'Duplicate hosted zone policy') {
    return value === 'Allow'
      ? 'Route 53 may create a hosted zone even when another hosted zone has the same domain name.'
      : 'Route 53 hosted zone creation is blocked when the account already has a hosted zone with the same domain name.';
  }
  if (label === 'Zone deletion policy') {
    return value === 'Delete'
      ? 'Deleting a Zone also deletes the matching provider zone when the controller can safely do so.'
      : 'Deleting a Zone leaves the provider zone in place.';
  }
  return '';
}

function ManifestTab({
  zoneClass,
  liveYaml,
  onCopy,
  onEdit,
}: {
  zoneClass: ZoneClass;
  liveYaml: string;
  onCopy: () => void;
  onEdit: () => void;
}) {
  const editPath = resourceYamlPath(zoneClass);
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

function HealthRow({
  label,
  health,
  reason,
  action,
  divided,
}: {
  label: string;
  health: Health;
  reason: string;
  action?: React.ReactNode;
  divided?: boolean;
}) {
  return (
    <Box
      sx={{
        alignItems: 'center',
        borderBottom: divided ? 1 : 0,
        borderColor: ui.borderSoft,
        display: 'grid',
        gap: 1.5,
        gridTemplateColumns: { xs: '1fr', md: '150px 90px 1fr auto' },
        minHeight: 45,
        py: 1.25,
      }}
    >
      <Typography sx={{ color: ui.text, fontSize: 13, fontWeight: 700 }}>{label}</Typography>
      <HealthInline health={health} />
      <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.5 }}>{reason}</Typography>
      <Box sx={{ justifySelf: { md: 'end' } }}>{action}</Box>
    </Box>
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

function resourceConditionsHealth(
  conditions: DnsCondition[] | undefined,
  requiredTypes: string[],
): Health {
  if (!conditions?.length) {
    return 'Syncing';
  }
  const required = requiredTypes.map(type => conditions.find(condition => condition.type === type));
  if (required.some(condition => condition?.status === 'False')) {
    return 'Degraded';
  }
  if (required.every(condition => condition?.status === 'True')) {
    return 'Healthy';
  }
  return 'Syncing';
}

function conditionSummary(conditions?: DnsCondition[]): string {
  if (!conditions?.length) {
    return 'No conditions observed';
  }
  const active = conditions.filter(condition => condition.status === 'True');
  return (active.length ? active : conditions).map(condition => condition.type).join(' · ');
}
