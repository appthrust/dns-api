/** @jsxRuntime classic */
import React from "react";
import { Box, Stack, Typography } from "../../components/primitives";
import { nameOf, namespaceOf, zoneClassIdentityName } from "../../resources";
import type { DnsCondition, ProviderIdentity, RecordSet, Zone, ZoneClass, ZoneUnit, ZoneUnitRecordSetSpec } from "../../types/resources";
import { type DnsData, ui } from "../common/ui";

export type Health = "Healthy" | "Syncing" | "Degraded";

export type ZoneUnitOwnership = {
  ownerItem?: ZoneUnitRecordSetSpec;
  ownsItem: boolean;
  label: string;
  ownerLabel: string;
  providerDeletion: string;
};

export function providerRefText(provider?: RecordSet["spec"]["provider"]) {
  return provider ? `${provider.name}/${provider.version}` : "";
}

export function aliasOptions(recordSet: RecordSet) {
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

export function ttlMode(recordSet: RecordSet) {
  if (aliasOptions(recordSet)) {
    return "alias";
  }
  if (recordSet.spec.options?.ttl === "Auto") {
    return "Auto";
  }
  return "-";
}

export function zoneUnitForZone(zone: Zone, data: DnsData) {
  return data.zoneUnits.items.find(
    (item) => namespaceOf(item) === namespaceOf(zone) && nameOf(item) === nameOf(zone),
  );
}

export function zoneUnitOwnership(
  recordSet: RecordSet,
  zoneUnit?: ZoneUnit,
): ZoneUnitOwnership {
  const refKey = recordSetClaimKey(namespaceOf(recordSet), nameOf(recordSet));
  const ownItem = zoneUnit?.spec.recordSets?.find(
    (item) => recordSetClaimKey(item.recordSetNamespace, item.recordSetName) === refKey,
  );
  if (ownItem) {
    const uid = recordSet.metadata?.uid;
    if (!uid || ownItem.recordSetUID !== uid) {
      return {
        ownerItem: ownItem,
        ownsItem: false,
        label: !uid || !ownItem.recordSetUID
          ? "Incarnation unverified"
          : "Different RecordSet incarnation",
        ownerLabel: `${ownItem.recordSetNamespace}/${ownItem.recordSetName}`,
        providerDeletion:
          "Provider cleanup is blocked until ownership is bound to this RecordSet UID. A missing or older receipt cannot authorize it.",
      };
    }
    return {
      ownerItem: ownItem,
      ownsItem: true,
      label: "Owned by this RecordSet",
      ownerLabel: `${ownItem.recordSetNamespace}/${ownItem.recordSetName}`,
      providerDeletion:
        "The controller can clean up only with provider ownership bound to this RecordSet UID; otherwise deletion waits.",
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

export function providerLabel(recordSet: RecordSet) {
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

export function findProviderIdentity(
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

export function recordSetHealth(recordSet: RecordSet): Health {
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

export function resourceConditionsHealth(
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

export function conditionSummary(conditions: DnsCondition[] | undefined) {
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

export function HealthInline({ health }: { health: Health }) {
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

export function VitalsHealth({ health }: { health: Health }) {
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

export function SmallBadge({
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
