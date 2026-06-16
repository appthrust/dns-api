/** @jsxRuntime classic */
import { Icon } from "../../components/Icon";
import { YamlCodeBlock } from "../../components/YamlCodeBlock";
import { Box } from "../../components/primitives";
import { IconButton } from "../../components/primitives";
import { Stack } from "../../components/primitives";
import { Typography } from "../../components/primitives";
import React from "react";
import { deleteByKey } from "../../api/dns";
import { useDnsPlatform } from "../../platform";
import {
  fqdnForRecordSet,
  nameOf,
  namespaceOf,
  zoneClassIdentityName,
  zoneClassRefNamespace,
} from "../../resources";
import type {
  DnsCondition,
  ProviderIdentity,
  RecordSet,
  Zone,
  ZoneClass,
  ZoneUnit,
  ZoneUnitRecordSetSpec,
} from "../../types/resources";
import {
  type BreadcrumbItem,
  CopyFeedbackButton,
  DataGridTable,
  DetailFieldGrid,
  DesignLinkButton,
  DnsData,
  eventsForResource,
  GridCell,
  GridHeader,
  GridRow,
  Page,
  Panel,
  ResourceConditionsPanel,
  ResourceEventsPanel,
  StatusBadge,
  tableHeaderSx,
  toYaml,
  ToolbarButton,
  ui,
  useNotice,
} from "../common/ui";
import { integrationDetailPath, zoneClassDetailPath } from "../platform/routes";
import { zonePath } from "./routes";

type RecordSetTab = "recordData" | "status" | "ownership" | "manifest";
type Health = "Healthy" | "Syncing" | "Degraded";

const recordSetTabItems: Array<{ id: RecordSetTab; label: string }> = [
  { id: "recordData", label: "Record data" },
  { id: "status", label: "Status" },
  { id: "ownership", label: "Ownership" },
  { id: "manifest", label: "Manifest" },
];

function providerRefText(provider?: RecordSet["spec"]["provider"]) {
  return provider ? `${provider.name}/${provider.version}` : "";
}

export function recordSetValue(recordSet: RecordSet) {
  const spec = recordSet.spec as RecordSet["spec"] & {
    options?: {
      alias?: {
        dnsName?: string;
        hostedZoneID?: string;
        evaluateTargetHealth?: boolean;
      };
    };
  };
  if (spec.options?.alias) {
    return `alias ${spec.options.alias.dnsName ?? "-"}`;
  }
  if (recordSet.spec.a?.addresses?.length) {
    return recordSet.spec.a.addresses.join(", ");
  }
  if (recordSet.spec.aaaa?.addresses?.length) {
    return recordSet.spec.aaaa.addresses.join(", ");
  }
  if (recordSet.spec.txt?.values?.length) {
    return recordSet.spec.txt.values.join(", ");
  }
  if (recordSet.spec.cname?.target) {
    return recordSet.spec.cname.target;
  }
  if (recordSet.spec.mx?.records?.length) {
    return recordSet.spec.mx.records
      .map((record) => `${record.preference ?? "-"} ${record.exchange ?? "-"}`)
      .join(", ");
  }
  if (recordSet.spec.caa?.records?.length) {
    return recordSet.spec.caa.records
      .map(
        (record) =>
          `${record.flags ?? "-"} ${record.tag ?? "-"} ${record.value ?? "-"}`,
      )
      .join(", ");
  }
  if (recordSet.spec.ns?.nameServers?.length) {
    return recordSet.spec.ns.nameServers.join(", ");
  }
  return JSON.stringify(recordSet.spec.options ?? {});
}

export function RecordSetDetailPage({
  zone,
  recordSet,
  data,
  onEdit,
  onDelete,
  breadcrumb,
}: {
  zone: Zone;
  recordSet: RecordSet;
  data: DnsData;
  onEdit: () => void;
  onDelete: () => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const platform = useDnsPlatform();
  const { showError, snackbar } = useNotice();
  const [activeTab, setActiveTab] = React.useState<RecordSetTab>("recordData");
  const fqdn = fqdnForRecordSet(recordSet, [zone]);
  const events = eventsForResource(data.events.items, recordSet);
  const zoneUnit = zoneUnitForZone(zone, data);
  const ownership = zoneUnitOwnership(recordSet, zoneUnit);
  const health = recordSetHealth(recordSet);
  const provider = providerLabel(recordSet);
  const zoneClass = data.zoneClasses.items.find(
    (item) =>
      namespaceOf(item) === zoneClassRefNamespace(zone) &&
      nameOf(item) === zone.spec.zoneClassRef.name,
  );
  const identity = findProviderIdentity(data.identities.items, zoneClass);
  const liveYaml = React.useMemo(() => toYaml(recordSet), [recordSet]);

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
      breadcrumb={breadcrumb}
      title={fqdn}
      description={`${namespaceOf(recordSet)}/${nameOf(recordSet)}`}
      actions={
        <>
          <ToolbarButton
            label="Edit"
            icon="mdi:pencil"
            tone="secondary"
            onClick={onEdit}
          />
          <RecordSetActionMenu onDelete={onDelete} />
        </>
      }
    >
      <Stack spacing={2}>
        <RecordSetVitalsStrip
          fqdn={fqdn}
          health={health}
          ownership={ownership}
          provider={provider}
          recordSet={recordSet}
          zone={zone}
          onShowStatus={() => setActiveTab("status")}
          onShowOwnership={() => setActiveTab("ownership")}
        />

        <RecordSetTabs
          activeTab={activeTab}
          conditionCount={recordSet.status?.conditions?.length ?? 0}
          eventCount={events.length}
          onChange={setActiveTab}
        />

        {activeTab === "recordData" ? (
          <RecordDataTab
            fqdn={fqdn}
            provider={provider}
            recordSet={recordSet}
            onCopy={copyText}
          />
        ) : null}

        {activeTab === "status" ? (
          <StatusTab
            events={events}
            eventsError={data.events.error}
            identity={identity}
            onOpenIdentity={
              identity
                ? () => platform.navigation.push(integrationDetailPath(identity))
                : undefined
            }
            onOpenZone={() => platform.navigation.push(zonePath(zone))}
            onOpenZoneClass={
              zoneClass
                ? () => platform.navigation.push(zoneClassDetailPath(zoneClass))
                : undefined
            }
            recordSet={recordSet}
            zone={zone}
            zoneClass={zoneClass}
            zoneUnit={zoneUnit}
          />
        ) : null}

        {activeTab === "ownership" ? (
          <OwnershipTab
            fqdn={fqdn}
            ownership={ownership}
            recordSet={recordSet}
            zone={zone}
            zoneUnit={zoneUnit}
          />
        ) : null}

        {activeTab === "manifest" ? (
          <ManifestTab
            liveYaml={liveYaml}
            onCopy={() => copyText(liveYaml)}
            onEdit={() => platform.liveYaml.open(recordSet)}
          />
        ) : null}
      </Stack>
      {snackbar}
    </Page>
  );
}

function RecordSetActionMenu({ onDelete }: { onDelete: () => void }) {
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
      if (event.key === "Escape") {
        setOpen(false);
      }
    }

    document.addEventListener("pointerdown", closeOnOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOnOutside);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [open]);

  return (
    <div ref={menuRef} style={{ position: "relative" }}>
      <IconButton
        aria-label="Resource actions"
        onClick={() => setOpen((current) => !current)}
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
            boxShadow: "0 12px 32px rgba(15, 23, 42, 0.16)",
            minWidth: 220,
            p: 0.75,
            position: "absolute",
            right: 0,
            top: 38,
            zIndex: 20,
          }}
        >
          <MenuButton
            danger
            onClick={() => {
              setOpen(false);
              onDelete();
            }}
          >
            Delete RecordSet
          </MenuButton>
        </Box>
      ) : null}
    </div>
  );
}

function MenuButton({
  children,
  danger,
  onClick,
}: {
  children: React.ReactNode;
  danger?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      type="button"
      style={{
        background: "transparent",
        border: 0,
        borderRadius: "var(--dns-ui-radius-sm, 4px)",
        color: danger
          ? "var(--dns-ui-danger, #b42318)"
          : "var(--dns-ui-text, #111827)",
        cursor: "pointer",
        display: "block",
        font: "inherit",
        padding: "8px 10px",
        textAlign: "left",
        whiteSpace: "nowrap",
        width: "100%",
      }}
    >
      {children}
    </button>
  );
}

function RecordSetVitalsStrip({
  fqdn,
  health,
  ownership,
  provider,
  recordSet,
  zone,
  onShowStatus,
  onShowOwnership,
}: {
  fqdn: string;
  health: Health;
  ownership: ZoneUnitOwnership;
  provider: string;
  recordSet: RecordSet;
  zone: Zone;
  onShowStatus: () => void;
  onShowOwnership: () => void;
}) {
  return (
    <Panel sx={{ overflow: "hidden" }}>
      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "1fr", md: "1fr 1.35fr 2.8fr" },
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
          <VitalsGroup label="Desired record">
            <Stack
              direction="row"
              spacing={1}
              alignItems="baseline"
              useFlexGap
              flexWrap="wrap"
            >
              <Typography
                sx={{
                  color: ui.text,
                  fontSize: 20,
                  fontWeight: 700,
                  lineHeight: 1,
                }}
              >
                {recordSet.spec.type}
              </Typography>
              <Typography sx={{ color: ui.faint, fontSize: 13 }}>
                TTL {recordSet.spec.ttl ?? ttlMode(recordSet)}
              </Typography>
            </Stack>
          </VitalsGroup>
        </VitalsColumn>
        <VitalsColumn>
          <VitalsGroup label="Record name">
            <VitalsValue>{recordSet.spec.name}</VitalsValue>
          </VitalsGroup>
          <VitalsGroup label="FQDN">
            <Typography
              sx={{
                color: ui.text,
                fontFamily: "monospace",
                fontSize: 14,
                fontWeight: 700,
                lineHeight: 1.45,
                wordBreak: "break-word",
              }}
            >
              {fqdn}
            </Typography>
          </VitalsGroup>
        </VitalsColumn>
        <VitalsColumn last>
          <VitalsGroup label="Zone">
            <VitalsValue>
              {namespaceOf(zone)}/{nameOf(zone)}
            </VitalsValue>
          </VitalsGroup>
          <VitalsGroup label="Provider">
            <Stack
              direction="row"
              spacing={1}
              alignItems="center"
              useFlexGap
              flexWrap="wrap"
            >
              <Typography
                sx={{ color: ui.text, fontSize: 16, fontWeight: 700 }}
              >
                {provider}
              </Typography>
              <Typography
                sx={{ color: ui.faint, fontFamily: "monospace", fontSize: 13 }}
              >
                {providerRefText(recordSet.spec.provider)}
              </Typography>
            </Stack>
          </VitalsGroup>
          <VitalsGroup label="ZoneUnit owner">
            <Stack spacing={0.75} alignItems="flex-start">
              <StatusBadge
                label={
                  ownership.ownsItem
                    ? "Owned by this RecordSet"
                    : ownership.label
                }
                tone={
                  ownership.ownsItem
                    ? "success"
                    : ownership.ownerItem
                      ? "danger"
                      : "pending"
                }
              />
              <DesignLinkButton onClick={onShowOwnership}>
                Details <Icon icon="mdi:arrow-right" width={13} />
              </DesignLinkButton>
            </Stack>
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
        borderRight: last ? 0 : { md: 1 },
        borderBottom: { xs: last ? 0 : 1, md: 0 },
        borderColor: ui.borderSoft,
        minHeight: 170,
        p: 2.5,
      }}
    >
      <Stack spacing={3}>{children}</Stack>
    </Box>
  );
}

function VitalsGroup({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <Box sx={{ minWidth: 0 }}>
      <Typography sx={tableHeaderSx}>{label}</Typography>
      <Box sx={{ mt: 1 }}>{children}</Box>
    </Box>
  );
}

function VitalsValue({ children }: { children: React.ReactNode }) {
  return (
    <Typography
      sx={{ color: ui.text, fontSize: 16, fontWeight: 700, lineHeight: 1.35 }}
    >
      {children}
    </Typography>
  );
}

function RecordSetTabs({
  activeTab,
  conditionCount,
  eventCount,
  onChange,
}: {
  activeTab: RecordSetTab;
  conditionCount: number;
  eventCount: number;
  onChange: (tab: RecordSetTab) => void;
}) {
  return (
    <Box sx={{ borderBottom: 1, borderColor: ui.border, overflowX: "auto" }}>
      <Stack direction="row" spacing={1.5} useFlexGap flexWrap="nowrap">
        {recordSetTabItems.map((item) => {
          const selected = item.id === activeTab;
          const count =
            item.id === "status" ? conditionCount + eventCount : undefined;
          return (
            <button
              key={item.id}
              onClick={() => onChange(item.id)}
              type="button"
              style={{
                alignItems: "center",
                background: "transparent",
                border: 0,
                borderBottom: selected
                  ? "2px solid var(--dns-ui-accent, #2563eb)"
                  : "2px solid transparent",
                color: selected
                  ? "var(--dns-ui-accent, #2563eb)"
                  : "var(--dns-ui-text-muted, #667085)",
                cursor: "pointer",
                display: "inline-flex",
                font: "inherit",
                fontSize: 13,
                fontWeight: selected ? 700 : 600,
                gap: 6,
                minHeight: 42,
                padding: "0 8px",
                whiteSpace: "nowrap",
              }}
            >
              {item.label}
              {count !== undefined ? (
                <SmallBadge selected={selected}>{count}</SmallBadge>
              ) : null}
            </button>
          );
        })}
      </Stack>
    </Box>
  );
}

function RecordDataTab({
  fqdn,
  provider,
  recordSet,
  onCopy,
}: {
  fqdn: string;
  provider: string;
  recordSet: RecordSet;
  onCopy: (value: string) => void;
}) {
  const recordFields = desiredRecordRows(recordSet);
  const providerOptions = providerOptionRows(recordSet);
  const providerStatus = providerStatusRows(recordSet);

  return (
    <Stack spacing={1.5}>
      <Panel sx={{ overflow: "hidden" }}>
        <Stack
          direction={{ xs: "column", md: "row" }}
          justifyContent="space-between"
          alignItems={{ xs: "stretch", md: "center" }}
          spacing={1.5}
          sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}
        >
          <Box sx={{ minWidth: 0 }}>
            <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
              Desired record
            </Typography>
            <Typography
              sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.5, mt: 0.5 }}
            >
              {fqdn}
            </Typography>
          </Box>
          <CopyFeedbackButton
            label="Copy value"
            copiedLabel="Copied"
            onCopy={() => onCopy(recordSetValue(recordSet))}
          />
        </Stack>
        <DataGridTable
          columns="minmax(150px,0.45fr) minmax(240px,1fr)"
          framed={false}
        >
          <GridHeader labels={[{ label: "Field" }, { label: "Value" }]} />
          {recordFields.map((row) => (
            <RecordSetInfoRow
              key={row.label}
              label={row.label}
              value={row.value}
            />
          ))}
        </DataGridTable>
      </Panel>

      {providerOptions.length ? (
        <InfoTablePanel title={`${provider} options`} rows={providerOptions} />
      ) : null}

      {providerStatus.length ? (
        <InfoTablePanel
          title={`${provider} observed record`}
          rows={providerStatus}
        />
      ) : null}

      {recordSet.spec.adoption ? (
        <Panel sx={{ p: 2.5 }}>
          <Typography
            sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1.5 }}
          >
            Adoption
          </Typography>
          <YamlCodeBlock code={toYaml(recordSet.spec.adoption)} />
        </Panel>
      ) : null}
    </Stack>
  );
}

function StatusTab({
  events,
  eventsError,
  identity,
  onOpenIdentity,
  onOpenZone,
  onOpenZoneClass,
  recordSet,
  zone,
  zoneClass,
  zoneUnit,
}: {
  events: ReturnType<typeof eventsForResource>;
  eventsError: unknown;
  identity?: ProviderIdentity;
  onOpenIdentity?: () => void;
  onOpenZone: () => void;
  onOpenZoneClass?: () => void;
  recordSet: RecordSet;
  zone: Zone;
  zoneClass?: ZoneClass;
  zoneUnit?: ZoneUnit;
}) {
  return (
    <Stack spacing={1.5}>
      <RelatedResourcesPanel
        identity={identity}
        onOpenIdentity={onOpenIdentity}
        onOpenZone={onOpenZone}
        onOpenZoneClass={onOpenZoneClass}
        zone={zone}
        zoneClass={zoneClass}
        zoneUnit={zoneUnit}
      />
      <ResourceConditionsPanel
        conditions={recordSet.status?.conditions ?? []}
      />
      <ResourceEventsPanel events={events} error={eventsError} />
      <Panel sx={{ p: 2.5 }}>
        <Typography
          sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1.5 }}
        >
          Provider status
        </Typography>
        <YamlCodeBlock
          code={toYaml(recordSet.status?.provider?.data ?? {})}
          emptyText="No provider status"
        />
      </Panel>
    </Stack>
  );
}

function RelatedResourcesPanel({
  identity,
  onOpenIdentity,
  onOpenZone,
  onOpenZoneClass,
  zone,
  zoneClass,
  zoneUnit,
}: {
  identity?: ProviderIdentity;
  onOpenIdentity?: () => void;
  onOpenZone: () => void;
  onOpenZoneClass?: () => void;
  zone: Zone;
  zoneClass?: ZoneClass;
  zoneUnit?: ZoneUnit;
}) {
  const identityName = zoneClassIdentityName(zoneClass);
  return (
    <Panel sx={{ p: 2.25 }}>
      <Typography
        sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1.5 }}
      >
        Related resources
      </Typography>
      <Stack spacing={0}>
        <RelatedResourceRow
          label="Zone"
          health={resourceConditionsHealth(zone.status?.conditions, [
            "Accepted",
            "Programmed",
          ])}
          reason={conditionSummary(zone.status?.conditions)}
          divided
          action={
            <DesignLinkButton onClick={onOpenZone}>
              View <Icon icon="mdi:arrow-right" width={14} />
            </DesignLinkButton>
          }
        />
        <RelatedResourceRow
          label="ZoneClass"
          health={
            zoneClass
              ? resourceConditionsHealth(zoneClass.status?.conditions, [
                  "Accepted",
                ])
              : "Syncing"
          }
          reason={
            zoneClass
              ? conditionSummary(zoneClass.status?.conditions)
              : `${zoneClassRefNamespace(zone)}/${zone.spec.zoneClassRef.name} not visible`
          }
          divided
          action={
            zoneClass ? (
              <DesignLinkButton onClick={onOpenZoneClass}>
                View <Icon icon="mdi:arrow-right" width={14} />
              </DesignLinkButton>
            ) : undefined
          }
        />
        <RelatedResourceRow
          label={identity?.kind ?? "Provider Identity"}
          health={
            identity
              ? resourceConditionsHealth(identity.status?.conditions, [
                  "Accepted",
                  "Ready",
                ])
              : "Syncing"
          }
          reason={
            identity
              ? conditionSummary(identity.status?.conditions)
              : identityName
                ? `${namespaceOf(zoneClass)}/${identityName} not visible`
                : "No identityRef"
          }
          divided
          action={
            identity ? (
              <DesignLinkButton onClick={onOpenIdentity}>
                View <Icon icon="mdi:arrow-right" width={14} />
              </DesignLinkButton>
            ) : undefined
          }
        />
        <RelatedResourceRow
          label="ZoneUnit"
          health={
            zoneUnit
              ? resourceConditionsHealth(zoneUnit.status?.conditions, ["Programmed"])
              : "Syncing"
          }
          reason={
            zoneUnit ? conditionSummary(zoneUnit.status?.conditions) : "ZoneUnit pending"
          }
        />
      </Stack>
    </Panel>
  );
}

function RelatedResourceRow({
  action,
  divided,
  health,
  label,
  reason,
}: {
  action?: React.ReactNode;
  divided?: boolean;
  health: Health;
  label: string;
  reason: string;
}) {
  return (
    <Box
      sx={{
        alignItems: "center",
        borderBottom: divided ? 1 : 0,
        borderColor: ui.borderSoft,
        display: "grid",
        gap: 1.5,
        gridTemplateColumns: { xs: "1fr", md: "180px 100px 1fr auto" },
        minHeight: 48,
        py: 1.25,
      }}
    >
      <Typography sx={{ color: ui.text, fontSize: 13, fontWeight: 700 }}>
        {label}
      </Typography>
      <HealthInline health={health} />
      <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.5 }}>
        {reason}
      </Typography>
      <Box sx={{ justifySelf: { md: "end" } }}>{action}</Box>
    </Box>
  );
}

function OwnershipTab({
  fqdn,
  ownership,
  recordSet,
  zone,
  zoneUnit,
}: {
  fqdn: string;
  ownership: ZoneUnitOwnership;
  recordSet: RecordSet;
  zone: Zone;
  zoneUnit?: ZoneUnit;
}) {
  return (
    <Stack spacing={1.5}>
      <Panel sx={{ p: 2.5 }}>
        <Typography
          sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1.5 }}
        >
          Provider effect
        </Typography>
        <Stack spacing={1.25}>
          <Box sx={{ alignSelf: "flex-start" }}>
            <StatusBadge
              label={
                ownership.ownsItem
                  ? "Provider record is owned"
                  : ownership.label
              }
              tone={
                ownership.ownsItem
                  ? "success"
                  : ownership.ownerItem
                    ? "danger"
                    : "pending"
              }
            />
          </Box>
          <Typography sx={{ color: ui.faint, fontSize: 14, lineHeight: 1.65 }}>
            {ownership.providerDeletion}
          </Typography>
        </Stack>
      </Panel>
      <InfoTablePanel
        title="Record identity"
        rows={[
          ["Zone", `${namespaceOf(zone)}/${nameOf(zone)}`],
          ["FQDN", fqdn],
          ["Record type", recordSet.spec.type],
          ["Record name", recordSet.spec.name],
          [
            "RecordSet resource",
            `${namespaceOf(recordSet)}/${nameOf(recordSet)}`,
          ],
        ]}
      />
      <InfoTablePanel
        title="ZoneUnit ownership"
        rows={[
          [
            "ZoneUnit",
            zoneUnit ? `${namespaceOf(zoneUnit)}/${nameOf(zoneUnit)}` : "-",
          ],
          ["Owner item", ownership.ownerLabel],
          ["Programmed", conditionSummary(zoneUnit?.status?.conditions)],
        ]}
      />
    </Stack>
  );
}

function ManifestTab({
  liveYaml,
  onCopy,
  onEdit,
}: {
  liveYaml: string;
  onCopy: () => void;
  onEdit: () => void;
}) {
  return (
    <Panel sx={{ p: 2.5 }}>
      <Stack
        direction="row"
        justifyContent="space-between"
        alignItems="center"
        spacing={1.5}
        sx={{ mb: 1.5 }}
      >
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
          Manifest
        </Typography>
        <Stack direction="row" spacing={0.5} useFlexGap flexWrap="wrap">
          <CopyFeedbackButton onCopy={onCopy} />
          <ToolbarButton
            label="Edit Manifest"
            icon="mdi:file-code-outline"
            onClick={onEdit}
          />
        </Stack>
      </Stack>
      <YamlCodeBlock code={liveYaml} maxHeight="none" />
    </Panel>
  );
}

type InfoRow = [string, React.ReactNode];

type ZoneUnitOwnership = {
  ownerItem?: ZoneUnitRecordSetSpec;
  ownsItem: boolean;
  label: string;
  ownerLabel: string;
  providerDeletion: string;
};

function InfoTablePanel({ title, rows }: { title: string; rows: InfoRow[] }) {
  return (
    <Panel sx={{ overflow: "hidden" }}>
      <Box sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}>
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
          {title}
        </Typography>
      </Box>
      <DataGridTable
        columns="minmax(150px,0.45fr) minmax(240px,1fr)"
        framed={false}
      >
        <GridHeader labels={[{ label: "Field" }, { label: "Value" }]} />
        {rows.map(([label, value]) => (
          <RecordSetInfoRow key={label} label={label} value={value} />
        ))}
      </DataGridTable>
    </Panel>
  );
}

function RecordSetInfoRow({
  label,
  value,
}: {
  label: string;
  value: React.ReactNode;
}) {
  return (
    <GridRow>
      <GridCell>
        <Typography sx={{ color: ui.faint, fontSize: 13, fontWeight: 700 }}>
          {label}
        </Typography>
      </GridCell>
      <GridCell>
        {typeof value === "string" ||
        typeof value === "number" ||
        typeof value === "boolean" ? (
          <Typography
            sx={{
              color: ui.text,
              fontFamily: "monospace",
              fontSize: 13,
              lineHeight: 1.55,
              wordBreak: "break-word",
            }}
          >
            {String(value)}
          </Typography>
        ) : (
          value
        )}
      </GridCell>
    </GridRow>
  );
}

function ValueList({
  values,
}: {
  values: Array<string | number | boolean | undefined>;
}) {
  const present = values.filter((value) => value !== undefined && value !== "");
  if (!present.length) {
    return <MutedText>-</MutedText>;
  }
  return (
    <Stack spacing={0.75}>
      {present.map((value, index) => (
        <Typography
          key={`${String(value)}-${index}`}
          sx={{
            color: ui.text,
            fontFamily: "monospace",
            fontSize: 13,
            lineHeight: 1.55,
            wordBreak: "break-word",
          }}
        >
          {String(value)}
        </Typography>
      ))}
    </Stack>
  );
}

function MutedText({ children }: { children: React.ReactNode }) {
  return (
    <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.55 }}>
      {children}
    </Typography>
  );
}

function desiredRecordRows(
  recordSet: RecordSet,
): Array<{ label: string; value: React.ReactNode }> {
  const rows: Array<{ label: string; value: React.ReactNode }> = [
    { label: "Owner name", value: recordSet.spec.name },
    { label: "Type", value: recordSet.spec.type },
    { label: "TTL", value: recordSet.spec.ttl ?? ttlMode(recordSet) },
  ];
  const alias = aliasOptions(recordSet);
  if (alias) {
    return [
      ...rows,
      { label: "Alias target DNS name", value: String(alias.dnsName ?? "-") },
      {
        label: "Alias target hosted zone ID",
        value: String(alias.hostedZoneID ?? "-"),
      },
      {
        label: "Evaluate target health",
        value: alias.evaluateTargetHealth ? "true" : "false",
      },
    ];
  }
  if (recordSet.spec.a?.addresses?.length) {
    rows.push({
      label: "IPv4 addresses",
      value: <ValueList values={recordSet.spec.a.addresses} />,
    });
  } else if (recordSet.spec.aaaa?.addresses?.length) {
    rows.push({
      label: "IPv6 addresses",
      value: <ValueList values={recordSet.spec.aaaa.addresses} />,
    });
  } else if (recordSet.spec.txt?.values?.length) {
    rows.push({
      label: "TXT values",
      value: <ValueList values={recordSet.spec.txt.values} />,
    });
  } else if (recordSet.spec.cname?.target) {
    rows.push({ label: "CNAME target", value: recordSet.spec.cname.target });
  } else if (recordSet.spec.mx?.records?.length) {
    rows.push({
      label: "MX records",
      value: (
        <Stack spacing={0.75}>
          {recordSet.spec.mx.records.map((record, index) => (
            <Typography
              key={`${record.preference}-${record.exchange}-${index}`}
              sx={{
                color: ui.text,
                fontFamily: "monospace",
                fontSize: 13,
                lineHeight: 1.55,
                wordBreak: "break-word",
              }}
            >
              {record.preference ?? "-"} {record.exchange ?? "-"}
            </Typography>
          ))}
        </Stack>
      ),
    });
  } else if (recordSet.spec.caa?.records?.length) {
    rows.push({
      label: "CAA records",
      value: (
        <Stack spacing={0.75}>
          {recordSet.spec.caa.records.map((record, index) => (
            <Typography
              key={`${record.flags}-${record.tag}-${record.value}-${index}`}
              sx={{
                color: ui.text,
                fontFamily: "monospace",
                fontSize: 13,
                lineHeight: 1.55,
                wordBreak: "break-word",
              }}
            >
              {record.flags ?? "-"} {record.tag ?? "-"} {record.value ?? "-"}
            </Typography>
          ))}
        </Stack>
      ),
    });
  } else if (recordSet.spec.ns?.nameServers?.length) {
    rows.push({
      label: "Name servers",
      value: <ValueList values={recordSet.spec.ns.nameServers} />,
    });
  } else {
    rows.push({ label: "Value", value: recordSetValue(recordSet) });
  }
  return rows;
}

function providerOptionRows(recordSet: RecordSet): InfoRow[] {
  const options = recordSet.spec.options ?? {};
  const rows: InfoRow[] = [];
  const alias = aliasOptions(recordSet);
  if (alias) {
    return rows;
  }
  if ("ttl" in options) {
    rows.push(["TTL mode", String(options.ttl ?? "-")]);
  }
  if ("proxied" in options) {
    rows.push([
      "Proxy status",
      Boolean(options.proxied) ? "Proxied" : "DNS only",
    ]);
  }
  if ("comment" in options) {
    rows.push(["Comment", String(options.comment ?? "-")]);
  }
  if (Array.isArray(options.tags)) {
    rows.push(["Tags", <ValueList values={options.tags.map(String)} />]);
  }
  return rows;
}

function providerStatusRows(recordSet: RecordSet): InfoRow[] {
  const data = recordSet.status?.provider?.data;
  if (!data) {
    return [];
  }
  const rows: InfoRow[] = [];
  const cloudflareRecords = (data as { records?: unknown }).records;
  if (Array.isArray(cloudflareRecords) && cloudflareRecords.length) {
    rows.push([
      "Cloudflare records",
      <YamlCodeBlock code={toYaml(cloudflareRecords)} maxHeight={260} />,
    ]);
  }
  return rows;
}

function aliasOptions(recordSet: RecordSet) {
  const alias = recordSet.spec.options?.alias;
  return isRecord(alias)
    ? (alias as {
        dnsName?: unknown;
        hostedZoneID?: unknown;
        evaluateTargetHealth?: unknown;
      })
    : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function ttlMode(recordSet: RecordSet) {
  if (aliasOptions(recordSet)) {
    return "alias";
  }
  if (recordSet.spec.options?.ttl === "Auto") {
    return "Auto";
  }
  return "-";
}

function zoneUnitForZone(zone: Zone, data: DnsData) {
  return data.zoneUnits.items.find(
    (item) => namespaceOf(item) === namespaceOf(zone) && nameOf(item) === nameOf(zone),
  );
}

function zoneUnitOwnership(
  recordSet: RecordSet,
  zoneUnit?: ZoneUnit,
): ZoneUnitOwnership {
  const refKey = recordSetClaimKey(namespaceOf(recordSet), nameOf(recordSet));
  const ownItem = zoneUnit?.spec.recordSets?.find(
    (item) => recordSetClaimKey(item.recordSetNamespace, item.recordSetName) === refKey,
  );
  if (ownItem) {
    return {
      ownerItem: ownItem,
      ownsItem: true,
      label: "Owned by this RecordSet",
      ownerLabel: `${ownItem.recordSetNamespace}/${ownItem.recordSetName}`,
      providerDeletion:
        "Provider record set will be deleted by the controller when this RecordSet is deleted.",
    };
  }
  const conflict = zoneUnit?.spec.recordSets?.find(
    (item) =>
      item.name === recordSet.spec.name &&
      (item.type === recordSet.spec.type ||
        item.type === "CNAME" ||
        recordSet.spec.type === "CNAME"),
  );
  if (!conflict) {
    return {
      ownsItem: false,
      label: "ZoneUnit pending",
      ownerLabel: "-",
      providerDeletion:
        "No ZoneUnit owner item is visible for this record identity yet.",
    };
  }
  return {
    ownerItem: conflict,
    ownsItem: false,
    label: "RecordSetConflict",
    ownerLabel: `${conflict.recordSetNamespace}/${conflict.recordSetName}`,
    providerDeletion:
      "Provider record set will not be deleted by this RecordSet.",
  };
}

function recordSetClaimKey(namespace: string, name: string) {
  return `${namespace}\0${name}`;
}

function providerLabel(recordSet: RecordSet) {
  const value =
    `${recordSet.apiVersion ?? ""} ${recordSet.kind ?? ""} ${providerRefText(recordSet.spec.provider)}`.toLowerCase();
  if (value.includes("cloudflare")) {
    return "Cloudflare";
  }
  if (value.includes("google")) {
    return "Google Cloud DNS";
  }
  return "AWS Route 53";
}

function findProviderIdentity(
  identities: ProviderIdentity[],
  zoneClass?: ZoneClass,
): ProviderIdentity | undefined {
  const identityName = zoneClassIdentityName(zoneClass);
  if (!zoneClass || !identityName) {
    return undefined;
  }
  return identities.find(
    (identity) =>
      namespaceOf(identity) === namespaceOf(zoneClass) &&
      nameOf(identity) === identityName,
  );
}

function recordSetHealth(recordSet: RecordSet): Health {
  const conditionHealth = resourceConditionsHealth(
    recordSet.status?.conditions,
    ["Accepted", "Programmed"],
  );
  if (conditionHealth !== "Healthy") {
    return conditionHealth;
  }
  const observed = recordSet.status?.observedGeneration;
  const generation = recordSet.metadata?.generation;
  if (
    observed !== undefined &&
    generation !== undefined &&
    observed < generation
  ) {
    return "Syncing";
  }
  return "Healthy";
}

function resourceConditionsHealth(
  conditions: DnsCondition[] | undefined,
  requiredTypes: string[],
): Health {
  if (!conditions?.length) {
    return "Syncing";
  }
  const required = requiredTypes.map((type) =>
    conditions.find((condition) => condition.type === type),
  );
  if (required.some((condition) => condition?.status === "False")) {
    return "Degraded";
  }
  if (required.every((condition) => condition?.status === "True")) {
    return "Healthy";
  }
  return "Syncing";
}

function conditionSummary(conditions: DnsCondition[] | undefined) {
  if (!conditions?.length) {
    return "No condition";
  }
  const condition =
    conditions.find((item) => item.status === "False") ??
    conditions.find((item) => item.status === "Unknown") ??
    conditions[0];
  if (!condition) {
    return "No condition";
  }
  return `${condition.type}: ${condition.reason || condition.status}`;
}

function HealthInline({ health }: { health: Health }) {
  const color =
    health === "Healthy"
      ? ui.successText
      : health === "Syncing"
        ? ui.warningText
        : ui.dangerText;
  return (
    <Stack direction="row" spacing={0.75} alignItems="center">
      <Box sx={{ bgcolor: color, borderRadius: 999, height: 8, width: 8 }} />
      <Typography sx={{ color, fontSize: 13, fontWeight: 700 }}>
        {health}
      </Typography>
    </Stack>
  );
}

function VitalsHealth({ health }: { health: Health }) {
  const color =
    health === "Healthy"
      ? ui.successText
      : health === "Syncing"
        ? ui.warningText
        : ui.dangerText;
  const dotColor =
    health === "Healthy" ? "success" : health === "Syncing" ? "warning" : "danger";
  return (
    <Stack direction="row" spacing={0.75} alignItems="center">
      <Box sx={{ bgcolor: dotColor, borderRadius: 999, height: 8, width: 8 }} />
      <Typography
        sx={{
          color,
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
        alignItems: "center",
        background: selected
          ? "var(--dns-ui-accent, #2563eb)"
          : "var(--dns-ui-surface-muted, #f6f8fb)",
        border: selected ? 0 : "1px solid var(--dns-ui-border, #d7dde8)",
        borderRadius: 999,
        color: selected
          ? "var(--dns-ui-on-accent, #ffffff)"
          : "var(--dns-ui-text-muted, #667085)",
        display: "inline-flex",
        fontSize: 11,
        fontWeight: 700,
        height: 20,
        justifyContent: "center",
        lineHeight: 1,
        minWidth: 20,
        padding: "0 6px",
      }}
    >
      {children}
    </span>
  );
}

export function RecordSetDeletePage({
  zone,
  recordSet,
  data,
  onBack,
  onDeleted,
  breadcrumb,
}: {
  zone: Zone;
  recordSet: RecordSet;
  data: DnsData;
  onBack: () => void;
  onDeleted: () => void;
  breadcrumb?: BreadcrumbItem[];
}) {
  const { showSuccess, showError, snackbar } = useNotice();
  const fqdn = fqdnForRecordSet(recordSet, [zone]);
  const ownership = zoneUnitOwnership(recordSet, zoneUnitForZone(zone, data));
  const providerDeletion = ownership.ownsItem
    ? "Provider record set will be deleted by the controller."
    : "Provider record set will not be deleted by this RecordSet.";

  async function submit() {
    try {
      await deleteByKey(
        data.recordSets.objects,
        namespaceOf(recordSet),
        nameOf(recordSet),
      );
      showSuccess("RecordSet deletion requested");
      onDeleted();
    } catch (error) {
      showError(error);
    }
  }

  return (
    <Page
      breadcrumb={breadcrumb}
      title="Delete RecordSet"
      description={`Delete ${fqdn} from this Zone.`}
      actions={
        <ToolbarButton
          label="Cancel"
          icon="mdi:close"
          tone="secondary"
          onClick={onBack}
        />
      }
    >
      <Panel
        sx={{
          bgcolor: ui.warningBgSoft,
          borderColor: ui.warningBorder,
          maxWidth: 720,
          p: 2.5,
        }}
      >
        <Stack spacing={2}>
          <Stack direction="row" spacing={1.5} alignItems="flex-start">
            <Icon icon="mdi:alert-outline" width={22} />
            <Box>
              <Typography
                sx={{ color: ui.warningText, fontSize: 14, fontWeight: 600 }}
              >
                Delete this RecordSet
              </Typography>
              <Typography
                sx={{
                  color: ui.warningText,
                  fontSize: 14,
                  lineHeight: 1.65,
                  mt: 1,
                }}
              >
                The controller deletes the provider record only when this
                RecordSet owns the ZoneUnit item.
              </Typography>
            </Box>
          </Stack>
          <DetailFieldGrid
            fields={[
              ["Target", `${namespaceOf(recordSet)}/${nameOf(recordSet)}`],
              ["FQDN", fqdn],
              ["Record type", recordSet.spec.type],
              ["Current value", recordSetValue(recordSet)],
              ["ZoneUnit ownership", ownership.ownerLabel],
              ["Provider effect", providerDeletion],
            ]}
          />
          {!ownership.ownsItem ? (
            <Panel
              sx={{
                bgcolor: ui.warningBgSoft,
                borderColor: ui.warningBorder,
                p: 2,
              }}
            >
              <Typography
                sx={{ color: ui.warningText, fontSize: 14, fontWeight: 600 }}
              >
                RecordSetConflict
              </Typography>
              <Typography
                sx={{
                  color: ui.warningText,
                  fontSize: 14,
                  lineHeight: 1.65,
                  mt: 1,
                }}
              >
                This RecordSet is not the ZoneUnit owner, so deleting it will
                not delete the provider-side record set.
              </Typography>
            </Panel>
          ) : null}
          <Box>
            <ToolbarButton
              label="Delete RecordSet"
              icon="mdi:delete"
              tone="danger"
              onClick={submit}
            />
          </Box>
        </Stack>
      </Panel>
      {snackbar}
    </Page>
  );
}
