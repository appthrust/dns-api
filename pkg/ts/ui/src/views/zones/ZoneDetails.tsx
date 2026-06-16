/** @jsxRuntime classic */
import React from 'react';
import { Icon } from '../../components/Icon';
import { YamlCodeBlock } from '../../components/YamlCodeBlock';
import { Box, Chip, Stack, TextField, Typography } from '../../components/primitives';
import { useAccess } from '../../api/dns';
import { useDnsPlatform } from '../../platform';
import {
  nameOf,
  namespaceOf,
  RecordSetResource,
  resourceKey,
  zoneClassIdentityName,
  zoneClassRefNamespace,
  zoneRefNamespace,
} from '../../resources';
import type { DnsCondition, ProviderIdentity, RecordSet, Zone, ZoneClass } from '../../types/resources';
import {
  CopyFeedbackButton,
  DataGridTable,
  descriptionOf,
  DesignLinkButton,
  DnsData,
  EmptyState,
  eventsForResource,
  GridCell,
  GridHeader,
  GridRow,
  namespaceLabelsYaml,
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
import { integrationDetailPath, zoneClassDetailPath } from '../platform/routes';
import { recordSetValue } from './RecordSetPages';

type ZoneTab = 'recordsets' | 'status' | 'access' | 'nameServers' | 'manifest';
type Health = 'Healthy' | 'Syncing' | 'Degraded';

const tabItems: Array<{ id: ZoneTab; label: string }> = [
  { id: 'recordsets', label: 'RecordSets' },
  { id: 'status', label: 'Status' },
  { id: 'access', label: 'Access' },
  { id: 'nameServers', label: 'Name servers' },
  { id: 'manifest', label: 'Manifest' },
];

export function ZoneDetails({
  zone,
  data,
  onCreateRecordSet,
  onOpenRecordSet,
}: {
  zone: Zone;
  data: DnsData;
  onCreateRecordSet: () => void;
  onOpenRecordSet: (recordSet: RecordSet) => void;
}) {
  const platform = useDnsPlatform();
  const { showError, snackbar } = useNotice();
  const [activeTab, setActiveTab] = React.useState<ZoneTab>('recordsets');
  const canCreateRecordSet = useAccess(RecordSetResource, 'create', {
    group: 'dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'recordsets',
  });
  const nameServers = zone.status?.nameServers ?? [];
  const recordSets = data.recordSets.items.filter(
    recordSet =>
      zoneRefNamespace(recordSet) === namespaceOf(zone) &&
      recordSet.spec.zoneRef.name === nameOf(zone)
  );
  const zoneClass = data.zoneClasses.items.find(
    item =>
      namespaceOf(item) === zoneClassRefNamespace(zone) &&
      nameOf(item) === zone.spec.zoneClassRef.name
  );
  const identity = findProviderIdentity(data.identities.items, zoneClass);
  const events = eventsForResource(data.events.items, zone);
  const provider = providerForResource(zoneClass);
  const openZoneClass = zoneClass
    ? () => platform.navigation.push(zoneClassDetailPath(zoneClass))
    : undefined;
  const openIdentity = identity
    ? () => platform.navigation.push(integrationDetailPath(identity))
    : undefined;
  const providerData = zone.status?.provider?.data as
    | { hostedZoneID?: string; zone?: { id?: string } }
    | undefined;
  const hostedZoneID =
    providerData?.hostedZoneID ??
    (zone.spec.adoption?.hostedZoneId as string | undefined);
  const cloudflareZoneID = providerData?.zone?.id ?? (zone.spec.adoption?.zoneID as string | undefined);
  const providerZoneID = provider === 'Cloudflare' ? cloudflareZoneID : hostedZoneID;
  const providerZoneIDLabel = provider === 'Cloudflare' ? 'Zone ID' : 'Hosted zone ID';
  const providerConsoleLabel =
    provider === 'Cloudflare' ? 'Open in Cloudflare' : 'Open in Route 53';
  const providerConsoleUrl =
    provider === 'Cloudflare'
      ? cloudflareZoneRecordsUrl(identity, zone.spec.domainName)
      : hostedZoneID
        ? route53HostedZoneUrl(hostedZoneID)
        : '';
  const recordSetHealth = recordSets.map(recordSet => recordSetStatus(recordSet));
  const recordHealthCounts = countHealth(recordSetHealth);
  const overallHealth = combineHealth([
    resourceConditionsHealth(zone.status?.conditions, ['Accepted', 'Programmed']),
    ...recordSetHealth,
    resourceConditionsHealth(zoneClass?.status?.conditions, ['Accepted']),
    resourceConditionsHealth(identity?.status?.conditions, ['Accepted', 'Ready']),
  ]);
  const typeOptions = recordTypeOptions(recordSets);
  const liveYaml = React.useMemo(() => toYaml(zone), [zone]);

  async function copyText(value: string) {
    try {
      await platform.clipboard.writeText(value);
    } catch (error) {
      showError(error);
      throw error;
    }
  }

  return (
    <>
      <Stack spacing={2}>
        <VitalsStrip
          health={overallHealth}
          recordCount={recordSets.length}
          recordHealthCounts={recordHealthCounts}
          zone={zone}
          zoneClass={zoneClass}
          provider={provider}
          providerConsoleLabel={providerConsoleLabel}
          providerConsoleUrl={providerConsoleUrl}
          providerZoneID={providerZoneID}
          providerZoneIDLabel={providerZoneIDLabel}
          onShowStatus={() => setActiveTab('status')}
          onOpenProviderConsole={() => platform.externalLinks.open(providerConsoleUrl)}
          onOpenZoneClass={openZoneClass}
        />

        <Tabs activeTab={activeTab} recordSetCount={recordSets.length} onChange={setActiveTab} />

        {activeTab === 'recordsets' ? (
          <RecordSetsTab
            recordSets={recordSets}
            typeOptions={typeOptions}
            canCreateRecordSet={canCreateRecordSet}
            onCreateRecordSet={onCreateRecordSet}
            onOpenRecordSet={onOpenRecordSet}
          />
        ) : null}

        {activeTab === 'status' ? (
          <StatusTab
            zone={zone}
            zoneClass={zoneClass}
            identity={identity}
            recordSets={recordSets}
            recordHealthCounts={recordHealthCounts}
            events={events}
            eventsError={data.events.error}
            onShowRecordSets={() => setActiveTab('recordsets')}
            onOpenZoneClass={openZoneClass}
            onOpenIdentity={openIdentity}
          />
        ) : null}

        {activeTab === 'access' ? (
          <AccessTab zone={zone} />
        ) : null}

        {activeTab === 'nameServers' ? (
          <NameServersTab
            nameServers={nameServers}
            onCopy={value => copyText(value)}
          />
        ) : null}

        {activeTab === 'manifest' ? (
          <ManifestTab
            zone={zone}
            liveYaml={liveYaml}
            onCopy={() => copyText(liveYaml)}
            onEdit={() => platform.liveYaml.open(zone)}
          />
        ) : null}
      </Stack>
      {snackbar}
    </>
  );
}

function VitalsStrip({
  health,
  recordCount,
  recordHealthCounts,
  zone,
  zoneClass,
  provider,
  providerConsoleLabel,
  providerConsoleUrl,
  providerZoneID,
  providerZoneIDLabel,
  onShowStatus,
  onOpenProviderConsole,
  onOpenZoneClass,
}: {
  health: Health;
  recordCount: number;
  recordHealthCounts: Record<Health, number>;
  zone: Zone;
  zoneClass?: ZoneClass;
  provider: string;
  providerConsoleLabel: string;
  providerConsoleUrl: string;
  providerZoneID?: string;
  providerZoneIDLabel: string;
  onShowStatus: () => void;
  onOpenProviderConsole: () => void;
  onOpenZoneClass?: () => void;
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
              <VitalsHealth health={health} />
              <DesignLinkButton onClick={onShowStatus}>
                Details <Icon icon="mdi:arrow-right" width={13} />
              </DesignLinkButton>
            </Stack>
          </VitalsGroup>
          <VitalsGroup label="RecordSets">
            <Stack direction="row" spacing={1.25} alignItems="center" useFlexGap flexWrap="wrap">
              <Typography sx={{ color: ui.text, fontSize: 20, fontWeight: 700, lineHeight: 1 }}>
                {recordCount}
              </Typography>
              <HealthCount tone="success" value={recordHealthCounts.Healthy} />
              <HealthCount tone="warning" value={recordHealthCounts.Syncing} />
              <HealthCount tone="danger" value={recordHealthCounts.Degraded} />
            </Stack>
          </VitalsGroup>
        </VitalsColumn>
        <VitalsColumn>
          <VitalsGroup label="Namespace">
            <VitalsValue>{namespaceOf(zone)}</VitalsValue>
          </VitalsGroup>
          <VitalsGroup label="Name">
            <VitalsValue>{nameOf(zone)}</VitalsValue>
          </VitalsGroup>
        </VitalsColumn>
        <VitalsColumn last>
          <VitalsGroup label="Provider">
            <Stack direction="row" alignItems="center" spacing={1.25} useFlexGap flexWrap="wrap">
              <Typography sx={{ color: ui.text, fontSize: 18, fontWeight: 700, lineHeight: 1.25 }}>
                {provider}
              </Typography>
              <AdoptionBadge adopted={Boolean(zone.spec.adoption)} />
              <DesignLinkButton disabled={!providerConsoleUrl} onClick={onOpenProviderConsole}>
                {providerConsoleLabel} <Icon icon="mdi:open-in-new" width={12} />
              </DesignLinkButton>
            </Stack>
            <Typography sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 13, mt: 0.75 }}>
              {providerZoneIDLabel}: {providerZoneID ?? '-'}
            </Typography>
          </VitalsGroup>
          <VitalsGroup label="Zone Class">
            <DesignLinkButton onClick={onOpenZoneClass}>
              {zoneClass
                ? resourceKey(zoneClass)
                : `${zoneClassRefNamespace(zone)}/${zone.spec.zoneClassRef.name}`}
            </DesignLinkButton>
          </VitalsGroup>
        </VitalsColumn>
      </Box>
    </Panel>
  );
}

function VitalsColumn({
  children,
  last,
}: {
  children: React.ReactNode;
  last?: boolean;
}) {
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

function Tabs({
  activeTab,
  recordSetCount,
  onChange,
}: {
  activeTab: ZoneTab;
  recordSetCount: number;
  onChange: (tab: ZoneTab) => void;
}) {
  return (
    <Box
      sx={{
        borderBottom: 1,
        borderColor: ui.border,
        overflowX: 'auto',
      }}
    >
      <Stack direction="row" spacing={1.5} useFlexGap flexWrap="nowrap">
        {tabItems.map(item => {
          const selected = item.id === activeTab;
          return (
            <button
              key={item.id}
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
              {item.id === 'recordsets' ? (
                <SmallBadge selected={selected}>{recordSetCount}</SmallBadge>
              ) : null}
            </button>
          );
        })}
      </Stack>
    </Box>
  );
}

function RecordSetsTab({
  recordSets,
  typeOptions,
  canCreateRecordSet,
  onCreateRecordSet,
  onOpenRecordSet,
}: {
  recordSets: RecordSet[];
  typeOptions: string[];
  canCreateRecordSet: boolean;
  onCreateRecordSet: () => void;
  onOpenRecordSet: (recordSet: RecordSet) => void;
}) {
  const [search, setSearch] = React.useState('');
  const [typeFilter, setTypeFilter] = React.useState('All');
  const searchUpdateRef = React.useRef<number | null>(null);
  const typeFilterUpdateRef = React.useRef<number | null>(null);
  const filteredRecordSets = React.useMemo(
    () => filterRecordSets(recordSets, search, typeFilter),
    [recordSets, search, typeFilter]
  );
  React.useEffect(
    () => () => {
      if (searchUpdateRef.current !== null) {
        window.clearTimeout(searchUpdateRef.current);
      }
      if (typeFilterUpdateRef.current !== null) {
        window.clearTimeout(typeFilterUpdateRef.current);
      }
    },
    []
  );

  React.useEffect(() => {
    if (!typeOptions.includes(typeFilter)) {
      setTypeFilter('All');
    }
  }, [typeFilter, typeOptions]);

  function updateSearch(value: string) {
    if (searchUpdateRef.current !== null) {
      window.clearTimeout(searchUpdateRef.current);
    }
    searchUpdateRef.current = window.setTimeout(() => {
      setSearch(value);
      searchUpdateRef.current = null;
    }, 0);
  }

  function updateTypeFilter(value: string) {
    if (typeFilterUpdateRef.current !== null) {
      window.clearTimeout(typeFilterUpdateRef.current);
    }
    typeFilterUpdateRef.current = window.setTimeout(() => {
      setTypeFilter(value);
      typeFilterUpdateRef.current = null;
    }, 0);
  }

  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Stack
        direction={{ xs: 'column', md: 'row' }}
        alignItems={{ xs: 'stretch', md: 'center' }}
        justifyContent="space-between"
        spacing={1.5}
        sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}
      >
        <Stack direction={{ xs: 'column', md: 'row' }} spacing={1} alignItems={{ md: 'center' }}>
          <TextField
            aria-label="Search RecordSets"
            defaultValue={search}
            onChange={(event: React.ChangeEvent<HTMLInputElement>) => updateSearch(event.target.value)}
            placeholder="Search records"
            sx={{ minWidth: { xs: '100%', md: 260 } }}
          />
          <Stack direction="row" spacing={0.5} useFlexGap flexWrap="wrap">
            {typeOptions.map(type => (
              <FilterChip
                key={type}
                label={type}
                selected={typeFilter === type}
                onClick={() => updateTypeFilter(type)}
              />
            ))}
          </Stack>
        </Stack>
        <Stack direction="row" alignItems="center" justifyContent="flex-end" spacing={1}>
          <Typography sx={{ color: ui.faint, fontSize: 13 }}>
            {filteredRecordSets.length} of {recordSets.length}
          </Typography>
          <ToolbarButton
            label="Add RecordSet"
            icon="mdi:plus"
            disabled={!canCreateRecordSet}
            onClick={onCreateRecordSet}
          />
        </Stack>
      </Stack>
      {filteredRecordSets.length ? (
        <>
          <Box sx={{ display: { xs: 'none', md: 'block' } }}>
            <DataGridTable
              columns="minmax(190px,1.05fr) minmax(90px,0.4fr) minmax(220px,1.2fr) minmax(80px,0.35fr) minmax(130px,0.55fr)"
              framed={false}
            >
              <GridHeader
                labels={[
                  { label: 'Name' },
                  { label: 'Type' },
                  { label: 'Value' },
                  { label: 'TTL' },
                  { label: 'Status' },
                ]}
              />
              {filteredRecordSets.map(recordSet => (
                <RecordSetDesktopRow
                  key={resourceKey(recordSet)}
                  recordSet={recordSet}
                  onOpenRecordSet={onOpenRecordSet}
                />
              ))}
            </DataGridTable>
          </Box>
          <Box sx={{ display: { xs: 'block', md: 'none' } }}>
            <DataGridTable
              columns="minmax(112px,1fr) minmax(44px,0.35fr) minmax(52px,0.4fr) minmax(96px,0.7fr)"
              framed={false}
            >
              <GridHeader
                labels={[
                  { label: 'Name' },
                  { label: 'Type' },
                  { label: 'TTL' },
                  { label: 'Status' },
                ]}
              />
              {filteredRecordSets.map(recordSet => (
                <RecordSetMobileRow
                  key={resourceKey(recordSet)}
                  recordSet={recordSet}
                  onOpenRecordSet={onOpenRecordSet}
                />
              ))}
            </DataGridTable>
          </Box>
        </>
      ) : (
        <EmptyState
          title="No RecordSets"
          body="Add a RecordSet from this Zone to publish DNS records."
          action={
            <ToolbarButton
              label="Add RecordSet"
              icon="mdi:plus"
              disabled={!canCreateRecordSet}
              onClick={onCreateRecordSet}
            />
          }
        />
      )}
    </Panel>
  );
}

function RecordSetDesktopRow({
  recordSet,
  onOpenRecordSet,
}: {
  recordSet: RecordSet;
  onOpenRecordSet: (recordSet: RecordSet) => void;
}) {
  return (
    <GridRow>
      <GridCell>
        <Stack spacing={0.25} sx={{ minWidth: 0 }}>
          <DesignLinkButton onClick={() => onOpenRecordSet(recordSet)}>
            {recordSet.spec.name}
          </DesignLinkButton>
          <Typography sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 12 }}>
            {namespaceOf(recordSet)}/{nameOf(recordSet)}
          </Typography>
        </Stack>
      </GridCell>
      <GridCell>
        <Typography sx={{ color: ui.text, fontSize: 14 }}>{recordSet.spec.type}</Typography>
      </GridCell>
      <GridCell>
        <Typography sx={{ color: ui.muted, fontSize: 14 }} noWrap>
          {recordSetValue(recordSet)}
        </Typography>
      </GridCell>
      <GridCell>
        <Typography sx={{ color: ui.text, fontSize: 14 }}>{recordSet.spec.ttl ?? '-'}</Typography>
      </GridCell>
      <GridCell>
        <HealthPill health={recordSetStatus(recordSet)} />
      </GridCell>
    </GridRow>
  );
}

function RecordSetMobileRow({
  recordSet,
  onOpenRecordSet,
}: {
  recordSet: RecordSet;
  onOpenRecordSet: (recordSet: RecordSet) => void;
}) {
  return (
    <GridRow>
      <GridCell>
        <Stack spacing={0.25} sx={{ minWidth: 0 }}>
          <DesignLinkButton onClick={() => onOpenRecordSet(recordSet)}>
            {recordSet.spec.name}
          </DesignLinkButton>
          <Typography sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 12 }}>
            {recordSetValue(recordSet)}
          </Typography>
          <Typography sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 12 }}>
            {namespaceOf(recordSet)}/{nameOf(recordSet)}
          </Typography>
        </Stack>
      </GridCell>
      <GridCell>
        <Typography sx={{ color: ui.text, fontSize: 14 }}>{recordSet.spec.type}</Typography>
      </GridCell>
      <GridCell>
        <Typography sx={{ color: ui.text, fontSize: 14 }}>{recordSet.spec.ttl ?? '-'}</Typography>
      </GridCell>
      <GridCell>
        <HealthPill health={recordSetStatus(recordSet)} />
      </GridCell>
    </GridRow>
  );
}

function StatusTab({
  zone,
  zoneClass,
  identity,
  recordSets,
  recordHealthCounts,
  events,
  eventsError,
  onShowRecordSets,
  onOpenZoneClass,
  onOpenIdentity,
}: {
  zone: Zone;
  zoneClass?: ZoneClass;
  identity?: ProviderIdentity;
  recordSets: RecordSet[];
  recordHealthCounts: Record<Health, number>;
  events: ReturnType<typeof eventsForResource>;
  eventsError: unknown;
  onShowRecordSets: () => void;
  onOpenZoneClass?: () => void;
  onOpenIdentity?: () => void;
}) {
  const zoneHealth = resourceConditionsHealth(zone.status?.conditions, ['Accepted', 'Programmed']);
  const recordSetsHealth = combineHealth(recordSets.map(recordSetStatus));
  const zoneClassHealth = resourceConditionsHealth(zoneClass?.status?.conditions, ['Accepted']);
  const identityHealth = resourceConditionsHealth(identity?.status?.conditions, ['Accepted', 'Ready']);
  return (
    <Stack spacing={1.5}>
      <Panel sx={{ p: 2.25 }}>
        <StatusPanelTitle>Health breakdown</StatusPanelTitle>
        <Stack spacing={0}>
          <HealthRow
            label="Zone"
            health={zoneHealth}
            reason={conditionSummary(zone.status?.conditions)}
            divided
          />
          <HealthRow
            label="RecordSets"
            health={recordSetsHealth}
            reason={recordSetsHealthReason(recordSets.length, recordHealthCounts)}
            divided
            action={
              <DesignLinkButton onClick={onShowRecordSets}>
                View <Icon icon="mdi:arrow-right" width={14} />
              </DesignLinkButton>
            }
          />
          <HealthRow
            label="ZoneClass"
            health={zoneClassHealth}
            reason={zoneClass ? conditionSummary(zoneClass.status?.conditions) : 'ZoneClass not visible'}
            divided
            action={
              zoneClass ? (
                <DesignLinkButton onClick={onOpenZoneClass}>
                  View <Icon icon="mdi:arrow-right" width={14} />
                </DesignLinkButton>
              ) : undefined
            }
          />
          <HealthRow
            label={identity?.kind ?? 'Provider identity'}
            health={identityHealth}
            reason={identity ? conditionSummary(identity.status?.conditions) : 'Identity not visible'}
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
      <ResourceConditionsPanel conditions={zone.status?.conditions ?? []} />
      <ResourceEventsPanel events={events} error={eventsError} />
      <AdoptionPanel zone={zone} />
      <Panel sx={{ p: 2.5 }}>
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1.5 }}>
          Provider status
        </Typography>
        <YamlCodeBlock code={toYaml(zone.status?.provider?.data ?? {})} emptyText="No provider status" />
      </Panel>
    </Stack>
  );
}

function AccessTab({ zone }: { zone: Zone }) {
  const platform = useDnsPlatform();
  const rules = zone.spec.allowedRecordSets ?? [];
  if (!rules.length) {
    return (
      <Panel>
        <EmptyState
          title="Only this namespace"
          body="RecordSets in the same namespace can use this Zone. No cross-namespace access rules are set."
        />
      </Panel>
    );
  }

  return (
    <Stack spacing={2} sx={{ maxWidth: 980 }}>
      {rules.map((rule, index) => (
        <Panel key={index} sx={{ overflow: 'hidden' }}>
          <Stack
            direction="row"
            justifyContent="space-between"
            alignItems="center"
            sx={{ bgcolor: ui.fieldBg, borderBottom: 1, borderColor: ui.borderSoft, px: 2, py: 1.5 }}
          >
            <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 700 }}>
              Rule {index + 1}
            </Typography>
            <Typography sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 13 }}>
              {rule.records.length} record {rule.records.length === 1 ? 'pattern' : 'patterns'}
            </Typography>
          </Stack>
          <Box
            sx={{
              display: 'grid',
              gridTemplateColumns: { xs: '1fr', md: '1.2fr 1fr' },
            }}
          >
            <Box sx={{ borderRight: { md: 1 }, borderColor: ui.borderSoft, minWidth: 0, p: 2 }}>
              <Stack direction="row" alignItems="center" justifyContent="space-between" spacing={1}>
                <Typography sx={tableHeaderSx}>Namespace labels</Typography>
                <CopyFeedbackButton
                  size="compact"
                  onCopy={() =>
                    platform.clipboard.writeText(namespaceLabelsYaml(rule.namespaces.selector.matchLabels))
                  }
                />
              </Stack>
              <Box sx={{ mt: 1.25 }}>
                <YamlCodeBlock
                  code={namespaceLabelsYaml(rule.namespaces.selector.matchLabels)}
                  emptyText="No namespace labels are required."
                  maxHeight="none"
                />
              </Box>
              {(Object.keys(rule.namespaces.selector.matchLabels ?? {}).length > 1) ? (
                <Typography sx={{ color: ui.faint, fontSize: 12, mt: 1 }}>
                  all labels must match
                </Typography>
              ) : null}
            </Box>
            <Box sx={{ minWidth: 0, p: 2 }}>
              <Typography sx={tableHeaderSx}>Allowed records</Typography>
              <Stack spacing={1.25} sx={{ mt: 1.5 }}>
                {rule.records.map((record, recordIndex) => (
                  <Box
                    key={`${record.name.pattern}-${record.types.join('-')}`}
                    sx={{
                      borderBottom: recordIndex === rule.records.length - 1 ? 0 : 1,
                      borderColor: ui.borderSoft,
                      pb: recordIndex === rule.records.length - 1 ? 0 : 1.25,
                    }}
                  >
                    <Stack direction="row" spacing={0.75} useFlexGap flexWrap="wrap">
                      {record.types.map(type => (
                        <Chip
                          key={type}
                          label={type}
                          sx={{
                            bgcolor: ui.fieldBg,
                            border: 0,
                            color: ui.muted,
                            fontSize: 12,
                            fontWeight: 700,
                            height: 24,
                          }}
                        />
                      ))}
                    </Stack>
                    <Box
                      sx={{
                        bgcolor: ui.fieldBg,
                        border: 1,
                        borderColor: ui.borderSoft,
                        borderRadius: 1,
                        mt: 1,
                        px: 1.25,
                        py: 0.75,
                      }}
                    >
                    <Typography sx={{ color: ui.text, fontFamily: 'monospace', fontSize: 13, lineHeight: 1.5, wordBreak: 'break-word' }}>
                      {record.name.pattern}
                    </Typography>
                    </Box>
                  </Box>
                ))}
              </Stack>
            </Box>
          </Box>
        </Panel>
      ))}
    </Stack>
  );
}

function NameServersTab({
  nameServers,
  onCopy,
}: {
  nameServers: string[];
  onCopy: (value: string) => void;
}) {
  if (!nameServers.length) {
    return (
      <Panel>
        <EmptyState title="No name servers" body="Name servers have not been reported yet." />
      </Panel>
    );
  }

  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}>
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>Name servers</Typography>
        <CopyFeedbackButton
          label="Copy all"
          copiedLabel="Copied"
          onCopy={() => onCopy(nameServers.join('\n'))}
        />
      </Stack>
      <Stack spacing={0} sx={{ p: 0 }}>
        {nameServers.map(server => (
          <Stack key={server} direction="row" alignItems="center" justifyContent="space-between" spacing={1} sx={{ borderBottom: 1, borderColor: ui.borderSoft, px: 2, py: 1.25 }}>
            <Typography sx={{ color: ui.text, fontFamily: 'monospace', fontSize: 14, lineHeight: 1.6, wordBreak: 'break-word' }}>
              {server}
            </Typography>
            <CopyFeedbackButton size="compact" onCopy={() => onCopy(server)} />
          </Stack>
        ))}
      </Stack>
    </Panel>
  );
}

function ManifestTab({
  zone,
  liveYaml,
  onCopy,
  onEdit,
}: {
  zone: Zone;
  liveYaml: string;
  onCopy: () => void;
  onEdit: () => void;
}) {
  const editPath = resourceYamlPath(zone);
  const action = (
    <Stack direction="row" spacing={0.5}>
      <CopyFeedbackButton onCopy={onCopy} />
      {editPath ? <ToolbarButton label="Edit Manifest" icon="mdi:file-code-outline" onClick={onEdit} /> : null}
    </Stack>
  );
  return (
    <Panel sx={{ p: 2.5 }}>
      <Stack direction="row" justifyContent="space-between" alignItems="center" spacing={1.5} sx={{ mb: 1.5 }}>
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
          Manifest
        </Typography>
        {action}
      </Stack>
      <YamlCodeBlock code={liveYaml} maxHeight="none" />
    </Panel>
  );
}

function AdoptionPanel({ zone }: { zone: Zone }) {
  const adopted = Boolean(zone.spec.adoption);
  return (
    <Panel sx={{ p: 2.5 }}>
      <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>Adoption</Typography>
      <Stack direction="row" spacing={1} alignItems="center" sx={{ mt: 1.5 }}>
        <AdoptionBadge adopted={adopted} />
        <Typography sx={{ color: ui.faint, fontSize: 14 }}>
          {adopted
            ? 'This Zone points to an existing provider zone.'
            : 'This Zone is created or managed by the controller.'}
        </Typography>
      </Stack>
      {adopted ? (
        <Box sx={{ mt: 2 }}>
          <YamlCodeBlock code={toYaml(zone.spec.adoption ?? {})} />
        </Box>
      ) : null}
    </Panel>
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

function HealthInline({ health }: { health: Health }) {
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
          fontSize: 13,
          fontWeight: 700,
        }}
      >
        {health}
      </Typography>
    </Stack>
  );
}

function VitalsHealth({ health }: { health: Health }) {
  return (
    <Stack direction="row" spacing={0.75} alignItems="center">
      <Box
        sx={{
          bgcolor: health === 'Healthy' ? 'success' : health === 'Syncing' ? 'warning' : 'danger',
          borderRadius: 999,
          height: 8,
          width: 8,
        }}
      />
      <Typography
        sx={{
          color:
            health === 'Healthy'
              ? 'var(--dns-ui-success, #138a5b)'
              : health === 'Syncing'
                ? ui.warningText
                : ui.dangerText,
          fontSize: 16,
          fontWeight: 700,
          lineHeight: 1.2,
        }}
      >
        {health}
      </Typography>
    </Stack>
  );
}

function StatusPanelTitle({ children }: { children: React.ReactNode }) {
  return (
    <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 700, mb: 1.5 }}>
      {children}
    </Typography>
  );
}

function HealthBadge({ health, label = health }: { health: Health; label?: string }) {
  const tone = health === 'Healthy' ? 'success' : health === 'Syncing' ? 'pending' : 'danger';
  return <StatusBadge label={label} tone={tone} />;
}

function HealthPill({ health, failingLabel }: { health: Health; failingLabel?: boolean }) {
  return (
    <HealthBadge
      health={health}
      label={failingLabel && health === 'Degraded' ? 'Failing' : health}
    />
  );
}

function AdoptionBadge({ adopted }: { adopted: boolean }) {
  return (
    <Chip
      label={adopted ? 'Adopted' : 'Not adopted'}
      sx={{
        bgcolor: ui.fieldBg,
        border: 1,
        borderColor: ui.border,
        color: ui.text,
        fontSize: 12,
        fontWeight: 600,
        height: 24,
      }}
    />
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

function HealthCount({
  value,
  tone,
}: {
  value: number;
  tone: 'success' | 'warning' | 'danger';
}) {
  const color = tone === 'success' ? 'success' : tone === 'warning' ? 'warning' : 'danger';
  const muted = value === 0;
  return (
    <Stack direction="row" spacing={0.5} alignItems="center">
      <Box sx={{ bgcolor: muted ? ui.faint : color, borderRadius: 999, height: 8, width: 8 }} />
      <Typography sx={{ color: muted ? ui.faint : ui.text, fontSize: 15, fontWeight: 700 }}>
        {value}
      </Typography>
    </Stack>
  );
}

function FilterChip({
  label,
  selected,
  onClick,
}: {
  label: string;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        background: selected
          ? 'color-mix(in srgb, var(--dns-ui-accent, #2563eb) 14%, var(--dns-ui-surface, #ffffff))'
          : 'var(--dns-ui-surface-muted, #f5f7fb)',
        border: '1px solid var(--dns-ui-border, #d7dde8)',
        borderRadius: 'var(--dns-ui-radius-sm, 4px)',
        color: selected ? 'var(--dns-ui-accent, #2563eb)' : 'var(--dns-ui-text, #111827)',
        cursor: 'pointer',
        font: 'inherit',
        fontSize: 13,
        fontWeight: selected ? 700 : 600,
        padding: '6px 9px',
      }}
    >
      {label}
    </button>
  );
}

function MonoText({ children }: { children: React.ReactNode }) {
  return <Typography sx={{ color: ui.text, fontFamily: 'monospace', fontSize: 14 }}>{children}</Typography>;
}

function resourceConditionsHealth(conditions: DnsCondition[] | undefined, requiredTypes: string[]): Health {
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

function recordSetStatus(recordSet: RecordSet): Health {
  const conditionHealth = resourceConditionsHealth(recordSet.status?.conditions, ['Accepted', 'Programmed']);
  if (conditionHealth !== 'Healthy') {
    return conditionHealth;
  }
  const observed = recordSet.status?.observedGeneration;
  const generation = recordSet.metadata?.generation;
  if (observed !== undefined && generation !== undefined && observed < generation) {
    return 'Syncing';
  }
  return 'Healthy';
}

function combineHealth(values: Health[]): Health {
  if (values.some(value => value === 'Degraded')) {
    return 'Degraded';
  }
  if (values.some(value => value === 'Syncing')) {
    return 'Syncing';
  }
  return 'Healthy';
}

function countHealth(values: Health[]): Record<Health, number> {
  return values.reduce<Record<Health, number>>(
    (counts, value) => ({ ...counts, [value]: counts[value] + 1 }),
    { Healthy: 0, Syncing: 0, Degraded: 0 }
  );
}

function conditionSummary(conditions?: DnsCondition[]): string {
  if (!conditions?.length) {
    return 'No conditions observed';
  }
  const active = conditions.filter(condition => condition.status === 'True');
  return (active.length ? active : conditions).map(condition => condition.type).join(' · ');
}

function recordSetsHealthReason(total: number, counts: Record<Health, number>): string {
  if (counts.Syncing || counts.Degraded) {
    return `${counts.Healthy} healthy, ${counts.Syncing} syncing, ${counts.Degraded} degraded`;
  }
  return `${counts.Healthy} / ${total} Accepted · Programmed`;
}

function recordTypeOptions(recordSets: RecordSet[]): string[] {
  const preferred = ['A', 'AAAA', 'CNAME', 'TXT', 'MX'];
  const observed = new Set(recordSets.map(recordSet => recordSet.spec.type));
  const ordered = preferred.filter(type => observed.has(type));
  const rest = [...observed].filter(type => !preferred.includes(type)).sort();
  return ['All', ...ordered, ...rest];
}

function filterRecordSets(recordSets: RecordSet[], search: string, typeFilter: string): RecordSet[] {
  const query = search.trim().toLowerCase();
  return recordSets.filter(recordSet => {
    if (typeFilter !== 'All' && recordSet.spec.type !== typeFilter) {
      return false;
    }
    if (!query) {
      return true;
    }
    return [
      recordSet.spec.name,
      recordSetValue(recordSet),
      namespaceOf(recordSet),
      nameOf(recordSet),
      `${namespaceOf(recordSet)}/${nameOf(recordSet)}`,
    ]
      .join(' ')
      .toLowerCase()
      .includes(query);
  });
}

function findProviderIdentity(
  identities: ProviderIdentity[],
  zoneClass?: ZoneClass
): ProviderIdentity | undefined {
  const identityName = zoneClassIdentityName(zoneClass);
  if (!zoneClass || !identityName) {
    return undefined;
  }
  return identities.find(
    identity => namespaceOf(identity) === namespaceOf(zoneClass) && nameOf(identity) === identityName
  );
}

function route53HostedZoneUrl(hostedZoneID: string): string {
  const normalizedHostedZoneID = hostedZoneID.trim().replace(/^\/?hostedzone\//, '');
  if (!normalizedHostedZoneID) {
    return '';
  }
  return `https://console.aws.amazon.com/route53/v2/hostedzones#ListRecordSets/${encodeURIComponent(
    normalizedHostedZoneID
  )}`;
}

function cloudflareZoneRecordsUrl(identity: ProviderIdentity | undefined, domainName: string): string {
  if (identity?.kind !== 'CloudflareIdentity') {
    return '';
  }
  const accountID = identity.status?.account?.id?.trim();
  if (!accountID || !domainName) {
    return '';
  }
  return `https://dash.cloudflare.com/${encodeURIComponent(accountID)}/${encodeURIComponent(
    domainName
  )}/dns/records`;
}
