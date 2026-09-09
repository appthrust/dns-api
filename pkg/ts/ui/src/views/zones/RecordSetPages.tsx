/** @jsxRuntime classic */
import { Icon } from "../../components/Icon";
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
  zoneClassRefNamespace,
} from "../../resources";
import type {
  RecordSet,
  Zone,
} from "../../types/resources";
import {
  type BreadcrumbItem,
  DetailFieldGrid,
  DesignLinkButton,
  DnsData,
  eventsForResource,
  Page,
  Panel,
  StatusBadge,
  tableHeaderSx,
  toYaml,
  ToolbarButton,
  ui,
  useNotice,
} from "../common/ui";
import { integrationDetailPath, zoneClassDetailPath } from "../platform/routes";
import { zonePath } from "./routes";
import { ManifestTab, OwnershipTab, RecordDataTab, StatusTab } from "./RecordSetDetailPanels";
import {
  findProviderIdentity, providerLabel, providerRefText, recordSetHealth,
  SmallBadge, ttlMode, VitalsHealth, zoneUnitForZone, zoneUnitOwnership,
  type Health, type ZoneUnitOwnership,
} from "./recordSetPresentation";

type RecordSetTab = "recordData" | "status" | "ownership" | "manifest";

const recordSetTabItems: Array<{ id: RecordSetTab; label: string }> = [
  { id: "recordData", label: "Record data" },
  { id: "status", label: "Status" },
  { id: "ownership", label: "Ownership" },
  { id: "manifest", label: "Manifest" },
];


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
            recordValue={recordSetValue(recordSet)}
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
  const providerDeletion = ownership.providerDeletion;

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
                Provider cleanup requires ownership bound to this RecordSet UID
                and current controller authority.
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
                {ownership.label}
              </Typography>
              <Typography
                sx={{
                  color: ui.warningText,
                  fontSize: 14,
                  lineHeight: 1.65,
                  mt: 1,
                }}
              >
                An absent or different RecordSet UID cannot authorize deletion
                of the provider-side record.
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
