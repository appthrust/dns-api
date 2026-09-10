/** @jsxRuntime classic */
import React from "react";
import { Icon } from "../../components/Icon";
import { YamlCodeBlock } from "../../components/YamlCodeBlock";
import { Box, Stack, Typography } from "../../components/primitives";
import { nameOf, namespaceOf, zoneClassIdentityName, zoneClassRefNamespace } from "../../resources";
import type { ProviderIdentity, RecordSet, Zone, ZoneClass, ZoneUnit } from "../../types/resources";
import { CopyFeedbackButton, DataGridTable, DesignLinkButton, eventsForResource, GridCell, GridHeader, GridRow, Panel, ResourceConditionsPanel, ResourceEventsPanel, StatusBadge, toYaml, ToolbarButton, ui } from "../common/ui";
import { aliasOptions, conditionSummary, HealthInline, resourceConditionsHealth, ttlMode, type Health, type ZoneUnitOwnership } from "./recordSetPresentation";

export function RecordDataTab({
  fqdn,
  provider,
  recordSet,
  recordValue,
  onCopy,
}: {
  fqdn: string;
  provider: string;
  recordSet: RecordSet;
  recordValue: string;
  onCopy: (value: string) => void;
}) {
  const recordFields = desiredRecordRows(recordSet, recordValue);
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
            onCopy={() => onCopy(recordValue)}
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

export function StatusTab({
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

export function OwnershipTab({
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
                  ? "Current RecordSet incarnation"
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

export function ManifestTab({
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
  recordValue: string,
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
    rows.push({ label: "Value", value: recordValue });
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
