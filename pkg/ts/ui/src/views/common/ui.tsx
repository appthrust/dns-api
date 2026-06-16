/** @jsxRuntime classic */
import { Icon } from '../../components/Icon';
import { YamlCodeBlock } from '../../components/YamlCodeBlock';
import { Accordion } from '../../components/primitives';
import { AccordionDetails } from '../../components/primitives';
import { AccordionSummary } from '../../components/primitives';
import { Alert } from '../../components/primitives';
import { Box } from '../../components/primitives';
import { Button } from '../../components/primitives';
import { Chip } from '../../components/primitives';
import { FormControl } from '../../components/primitives';
import { IconButton } from '../../components/primitives';
import { Paper } from '../../components/primitives';
import { Snackbar } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { TextField } from '../../components/primitives';
import { Tooltip } from '../../components/primitives';
import { Typography } from '../../components/primitives';
import * as yaml from 'js-yaml';
import React from 'react';
import { useForm, type FieldValues, type UseFormReturn } from 'react-hook-form';
import { useDnsData } from '../../api/dns';
import { getDnsPlatform, useDnsPlatform } from '../../platform';
import { nameOf, namespaceOf, resourceKey, type KubeObjectInterface } from '../../resources';
import type { DnsCondition, KubeEvent, ProviderIdentity, ZoneClass } from '../../types/resources';

export type Notice = {
  severity: 'success' | 'error';
  message: string;
};

export type DnsData = ReturnType<typeof useDnsData>;

export function useDnsFormState<T extends FieldValues>(
  initialValues: T
): [T, React.Dispatch<React.SetStateAction<T>>, UseFormReturn<T>] {
  const formMethods = useForm<T>({ defaultValues: initialValues as never });
  const initialValuesKey = JSON.stringify(initialValues);

  React.useEffect(() => {
    formMethods.reset(initialValues);
  }, [formMethods, initialValuesKey]);

  const values = formMethods.watch() as T;
  const setValues = React.useCallback<React.Dispatch<React.SetStateAction<T>>>(
    nextValues => {
      const currentValues = formMethods.getValues() as T;
      formMethods.reset(
        typeof nextValues === 'function'
          ? (nextValues as (currentValue: T) => T)(currentValues)
          : nextValues
      );
    },
    [formMethods]
  );
  return [values, setValues, formMethods];
}

export const jsonIndent = 2;

export const ui = {
  appBg: 'surfaceMuted',
  sidebarBg: 'surface',
  panelBg: 'surface',
  panelBgSoft: 'surfaceMuted',
  rowHover: 'rowHover',
  fieldBg: 'fieldBg',
  border: 'border',
  borderSoft: 'borderSoft',
  text: 'text',
  muted: 'textMuted',
  faint: 'textMuted',
  accent: 'accent',
  accentStrong: 'accentStrong',
  successBg: 'successSurface',
  successText: 'success',
  warningBg: 'warningSurface',
  warningBgSoft: 'warningSurface',
  warningBorder: 'warning',
  warningText: 'warning',
  dangerBg: 'dangerSurface',
  dangerBorder: 'danger',
  dangerText: 'dangerText',
};

export function withOpacity(color: string, opacity: number) {
  const hex = color.replace('#', '');

  if (/^[\da-f]{3}$/i.test(hex)) {
    const [r, g, b] = hex.split('').map(value => Number.parseInt(`${value}${value}`, 16));
    return `rgba(${r}, ${g}, ${b}, ${opacity})`;
  }

  if (/^[\da-f]{6}$/i.test(hex)) {
    const r = Number.parseInt(hex.slice(0, 2), 16);
    const g = Number.parseInt(hex.slice(2, 4), 16);
    const b = Number.parseInt(hex.slice(4, 6), 16);
    return `rgba(${r}, ${g}, ${b}, ${opacity})`;
  }

  const rgb = color.match(/^rgba?\(([^)]+)\)$/i);
  if (rgb) {
    const [r, g, b] = (rgb[1] ?? '').split(',').map(value => value.trim());
    return `rgba(${r}, ${g}, ${b}, ${opacity})`;
  }

  return `color-mix(in srgb, ${color} ${Math.round(opacity * 100)}%, transparent)`;
}

export const selectedBg = 'selected';
export const selectedHoverBg = 'selectedHover';

export const tableHeaderSx = {
  color: ui.faint,
  fontSize: 11,
  fontWeight: 700,
  letterSpacing: 0.5,
  textTransform: 'uppercase',
};

export const tableCellSx = {
  alignItems: 'center',
  display: 'flex',
  minWidth: 0,
  px: 1.75,
  py: 1.5,
};

export function Page({
  title,
  description,
  actions,
  breadcrumb,
  children,
}: {
  title: string;
  description?: string;
  actions?: React.ReactNode;
  breadcrumb?: BreadcrumbItem[];
  children: React.ReactNode;
}) {
  return (
    <Box
      sx={{
        bgcolor: ui.appBg,
        color: ui.text,
        minHeight: '100vh',
        px: { xs: 2, md: 3 },
        py: 3,
      }}
    >
      <Stack spacing={2.5}>
        <PageHeader
          breadcrumbs={breadcrumb}
          title={title}
          description={description}
          actions={actions}
        />
        {children}
      </Stack>
    </Box>
  );
}

export function PageHeader({
  breadcrumbs,
  title,
  description,
  actions,
}: {
  breadcrumbs?: BreadcrumbItem[];
  title: string;
  description?: string;
  actions?: React.ReactNode;
}) {
  return (
    <Stack spacing={1.25}>
      {breadcrumbs && breadcrumbs.length ? <Breadcrumb items={breadcrumbs} /> : null}
      <Stack
        direction={{ xs: 'column', md: 'row' }}
        justifyContent="space-between"
        alignItems={{ xs: 'stretch', md: 'flex-start' }}
        spacing={2}
      >
        <Box sx={{ minWidth: 0 }}>
          <Typography sx={{ color: ui.text, fontSize: 26, fontWeight: 600, lineHeight: 1.2 }}>
            {title}
          </Typography>
          {description ? (
            <Typography
              sx={{ color: ui.muted, fontSize: 14, lineHeight: 1.65, mt: 1, maxWidth: 820 }}
            >
              {description}
            </Typography>
          ) : null}
        </Box>
        {actions ? (
          <Stack
            direction="row"
            justifyContent={{ xs: 'flex-start', md: 'flex-end' }}
            spacing={1}
            useFlexGap
            flexWrap="wrap"
            sx={{ maxWidth: '100%' }}
          >
            {actions}
          </Stack>
        ) : null}
      </Stack>
    </Stack>
  );
}

export function PageTitle(props: {
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <PageHeader title={props.title} description={props.description} actions={props.action} />
  );
}

export type BreadcrumbItem = {
  label: string;
  path?: string;
  loading?: boolean;
};

const breadcrumbEllipsisSx = {
  display: 'inline-block',
  maxWidth: 220,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  verticalAlign: 'bottom',
  whiteSpace: 'nowrap',
};

function BreadcrumbLabel({
  item,
  onNavigate,
}: {
  item: BreadcrumbItem;
  onNavigate: (path: string) => void;
}) {
  if (item.loading) {
    return <Typography sx={{ color: ui.faint, fontSize: 13 }}>…</Typography>;
  }
  if (item.path) {
    const path = item.path;
    return (
      <Button
        variant="text"
        title={item.label}
        onClick={() => onNavigate(path)}
        sx={{
          color: ui.muted,
          fontSize: 13,
          fontWeight: 600,
          minWidth: 0,
          p: 0,
          textTransform: 'none',
          ...breadcrumbEllipsisSx,
          '&:hover': { bgcolor: 'transparent', color: ui.accent, textDecoration: 'underline' },
        }}
      >
        {item.label}
      </Button>
    );
  }
  return (
    <Typography
      title={item.label}
      sx={{ color: ui.faint, fontSize: 13, fontWeight: 600, ...breadcrumbEllipsisSx }}
    >
      {item.label}
    </Typography>
  );
}

export function Breadcrumb({ items }: { items: BreadcrumbItem[] }) {
  const platform = useDnsPlatform();
  if (!items.length) {
    return null;
  }
  return (
    <Box component="nav" aria-label="breadcrumb" sx={{ minWidth: 0 }}>
      <Box
        component="ol"
        sx={{
          alignItems: 'center',
          display: 'flex',
          flexWrap: 'wrap',
          gap: '6px',
          listStyle: 'none',
          m: 0,
          minWidth: 0,
          p: 0,
        }}
      >
        {items.map((item, index) => (
          <Box
            component="li"
            key={`${index}-${item.label}`}
            sx={{ alignItems: 'center', display: 'flex', gap: '6px', minWidth: 0 }}
          >
            {index > 0 ? (
              <Typography
                aria-hidden="true"
                sx={{ color: ui.faint, fontSize: 13, userSelect: 'none' }}
              >
                ›
              </Typography>
            ) : null}
            <BreadcrumbLabel item={item} onNavigate={platform.navigation.push} />
          </Box>
        ))}
      </Box>
    </Box>
  );
}

export function ToolbarButton({
  label,
  icon,
  onClick,
  href,
  disabled,
  tone = 'primary',
}: {
  label: string;
  icon: string;
  onClick?: () => void;
  href?: string;
  disabled?: boolean;
  tone?: 'primary' | 'secondary' | 'danger';
}) {
  const linkProps = href ? { href } : {};
  return (
    <Button
      {...linkProps}
      startIcon={<Icon icon={icon} />}
      variant={tone === 'primary' ? 'contained' : 'outlined'}
      color={tone === 'danger' ? 'error' : 'primary'}
      size="small"
      onClick={onClick}
      disabled={disabled}
      sx={{
        borderRadius: 1,
        boxShadow: 'none',
        fontSize: 14,
        fontWeight: 600,
        lineHeight: 1.35,
        textTransform: 'none',
        '&:hover': { boxShadow: 'none' },
      }}
    >
      {label}
    </Button>
  );
}

export function RowAction({
  label,
  icon,
  onClick,
  disabled,
}: {
  label: string;
  icon: string;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <Tooltip title={label}>
      <span>
        <IconButton size="small" onClick={onClick} disabled={disabled} aria-label={label}>
          <Icon icon={icon} />
        </IconButton>
      </span>
    </Tooltip>
  );
}

export function CopyFeedbackButton({
  label = 'Copy',
  copiedLabel = 'Copied',
  onCopy,
  disabled,
  size = 'regular',
}: {
  label?: string;
  copiedLabel?: string;
  onCopy: () => Promise<unknown> | unknown;
  disabled?: boolean;
  size?: 'compact' | 'regular';
}) {
  const [copied, setCopied] = React.useState(false);
  const copiedTimeoutRef = React.useRef<number | null>(null);
  const isCompact = size === 'compact';

  React.useEffect(
    () => () => {
      if (copiedTimeoutRef.current !== null) {
        window.clearTimeout(copiedTimeoutRef.current);
      }
    },
    []
  );

  async function copy() {
    if (disabled) {
      return;
    }
    try {
      await onCopy();
      setCopied(true);
      if (copiedTimeoutRef.current !== null) {
        window.clearTimeout(copiedTimeoutRef.current);
      }
      copiedTimeoutRef.current = window.setTimeout(() => {
        setCopied(false);
        copiedTimeoutRef.current = null;
      }, 1800);
    } catch {
      setCopied(false);
    }
  }

  return (
    <button
      type="button"
      aria-live="polite"
      disabled={disabled}
      onClick={copy}
      style={{
        alignItems: 'center',
        background: 'var(--dns-ui-surface, #ffffff)',
        border: '1px solid var(--dns-ui-border, #d8dee8)',
        borderRadius: 'var(--dns-ui-radius-sm, 4px)',
        color: 'var(--dns-ui-accent, #2563eb)',
        cursor: disabled ? 'not-allowed' : 'pointer',
        display: 'inline-flex',
        font: 'inherit',
        fontSize: isCompact ? 12 : 14,
        fontWeight: isCompact ? 700 : 600,
        gap: isCompact ? 5 : 8,
        height: isCompact ? 26 : undefined,
        lineHeight: isCompact ? 1 : 1.35,
        opacity: disabled ? 0.55 : 1,
        padding: isCompact ? '0 10px' : '7px 12px',
      }}
    >
      <Icon icon="mdi:content-copy" width={isCompact ? 13 : 16} />
      {copied ? copiedLabel : label}
    </button>
  );
}

export function CopyableYamlSnippet({
  value,
  emptyText = 'No namespace labels are required.',
}: {
  value: string;
  emptyText?: string;
}) {
  const platform = useDnsPlatform();
  const canCopy = Boolean(value.trim());

  async function copySnippet() {
    if (!canCopy) {
      return;
    }
    await platform.clipboard.writeText(value);
  }

  const copyAction = (
    <CopyFeedbackButton disabled={!canCopy} onCopy={copySnippet} />
  );

  return (
    <Box sx={{ mt: 1.5 }}>
      <YamlCodeBlock code={value} emptyText={emptyText} action={copyAction} />
    </Box>
  );
}

export type StatusBadgeTone = 'success' | 'warning' | 'danger' | 'pending' | 'neutral' | 'unknown';

const statusBadgeToneStyles: Record<
  StatusBadgeTone,
  { bg: string; color: string; icon: string }
> = {
  success: {
    bg: ui.successBg,
    color: ui.successText,
    icon: 'mdi:check-circle-outline',
  },
  warning: {
    bg: ui.warningBg,
    color: ui.warningText,
    icon: 'mdi:alert-circle-outline',
  },
  danger: {
    bg: ui.dangerBg,
    color: ui.dangerText,
    icon: 'mdi:alert-outline',
  },
  pending: {
    bg: ui.warningBg,
    color: ui.warningText,
    icon: 'mdi:progress-clock',
  },
  neutral: {
    bg: ui.fieldBg,
    color: ui.text,
    icon: 'mdi:circle-outline',
  },
  unknown: {
    bg: ui.fieldBg,
    color: ui.muted,
    icon: 'mdi:help-circle-outline',
  },
};

export function StatusBadge({
  label,
  tone = 'neutral',
  icon,
}: {
  label: string;
  tone?: StatusBadgeTone;
  icon?: string;
}) {
  const style = statusBadgeToneStyles[tone];
  return (
    <Box
      component="span"
      sx={{
        alignItems: 'center',
        bgcolor: style.bg,
        border: 0,
        borderRadius: 999,
        color: style.color,
        display: 'inline-flex',
        fontSize: 12,
        fontWeight: 700,
        gap: 0,
        lineHeight: 1,
        minHeight: 26,
        px: 1,
        py: 0,
        whiteSpace: 'nowrap',
      }}
    >
      <Box
        component="span"
        sx={{
          alignItems: 'center',
          display: 'inline-flex',
          flex: '0 0 auto',
          height: 14,
          width: 14,
        }}
      >
        <Icon icon={icon ?? style.icon} width={14} />
      </Box>
      <Box component="span" sx={{ ml: 0.5 }}>
        {label}
      </Box>
    </Box>
  );
}

export function ConditionChip({ condition }: { condition?: DnsCondition }) {
  if (!condition) {
    return <StatusBadge label="Missing" tone="unknown" />;
  }
  const ok = condition.status === 'True';
  const label = ok
    ? condition.type
    : condition.type === 'Ready'
    ? 'Not ready'
    : condition.type === 'Accepted'
    ? 'Not accepted'
    : `Not ${condition.type.toLowerCase()}`;
  return (
    <Tooltip title={condition.message || condition.reason || condition.type}>
      <span>
        <StatusBadge label={label} tone={ok ? 'success' : 'danger'} />
      </span>
    </Tooltip>
  );
}

export function Conditions({ conditions }: { conditions?: DnsCondition[] }) {
  if (!conditions?.length) {
    return <Typography sx={{ color: ui.faint, fontSize: 14 }}>No conditions</Typography>;
  }
  return (
    <Stack direction="row" spacing={1} useFlexGap flexWrap="wrap">
      {conditions.map(condition => (
        <ConditionChip key={condition.type} condition={condition} />
      ))}
    </Stack>
  );
}

export function ConditionsTable({ conditions }: { conditions: DnsCondition[] }) {
  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Box sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}>
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>Conditions</Typography>
      </Box>
      {conditions.length ? (
        <Box>
          <Box
            sx={{
              borderBottom: 1,
              borderColor: ui.borderSoft,
              display: 'grid',
              gridTemplateColumns: {
                xs: '1fr',
                md: 'minmax(150px,0.6fr) minmax(150px,0.65fr) minmax(260px,1fr) minmax(180px,0.75fr)',
              },
              px: 0.5,
              py: 0.75,
            }}
          >
            {['Condition', 'Reason', 'Message', 'Last transition time'].map(label => (
              <GridCell key={label}>
                <Typography sx={tableHeaderSx}>{label}</Typography>
              </GridCell>
            ))}
          </Box>
          {conditions.map(condition => (
            <Box
              key={condition.type}
              sx={{
                borderBottom: 1,
                borderColor: ui.borderSoft,
                display: 'grid',
                gridTemplateColumns: {
                  xs: '1fr',
                  md: 'minmax(150px,0.6fr) minmax(150px,0.65fr) minmax(260px,1fr) minmax(180px,0.75fr)',
                },
                '&:last-child': { borderBottom: 0 },
              }}
            >
              <GridCell>
                <ConditionChip condition={condition} />
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.text, fontSize: 14 }}>
                  {condition.reason || '-'}
                </Typography>
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.faint, fontSize: 14, lineHeight: 1.6 }}>
                  {condition.message || '-'}
                </Typography>
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.faint, fontSize: 14 }}>
                  {condition.lastTransitionTime || '-'}
                </Typography>
              </GridCell>
            </Box>
          ))}
        </Box>
      ) : (
        <Box sx={{ px: 2, py: 2 }}>
          <Typography sx={{ color: ui.faint, fontSize: 14 }}>No conditions</Typography>
        </Box>
      )}
    </Panel>
  );
}

export function ResourceConditionsPanel({ conditions }: { conditions: DnsCondition[] }) {
  return (
    <Panel sx={{ p: 2.25 }}>
      <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1.5 }}>
        Conditions
      </Typography>
      {conditions.length ? (
        <Stack spacing={0}>
          {conditions.map((condition, index) => {
            const transitionTime = relativeTime(condition.lastTransitionTime);
            const tone = conditionStatusTone(condition.status);
            return (
              <Box
                key={condition.type}
                sx={{
                  alignItems: 'flex-start',
                  borderBottom: index === conditions.length - 1 ? 0 : 1,
                  borderColor: ui.borderSoft,
                  display: 'grid',
                  gap: 1.25,
                  gridTemplateColumns: '18px 1fr auto',
                  py: 1.35,
                }}
              >
                <Icon icon={tone.icon} color={tone.color} width={14} />
                <Box sx={{ minWidth: 0 }}>
                  <Typography
                    sx={{
                      color: ui.text,
                      fontSize: 13,
                      fontWeight: 700,
                      lineHeight: 1.35,
                    }}
                  >
                    {condition.type}
                  </Typography>
                  <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.5 }}>
                    {conditionDetail(condition)}
                  </Typography>
                </Box>
                {transitionTime ? (
                  <Typography sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 12 }}>
                    {transitionTime}
                  </Typography>
                ) : null}
              </Box>
            );
          })}
        </Stack>
      ) : (
        <Typography sx={{ color: ui.faint, fontSize: 13 }}>No conditions</Typography>
      )}
    </Panel>
  );
}

function conditionStatusTone(status: DnsCondition['status']) {
  if (status === 'True') {
    return {
      icon: 'mdi:check-circle-outline',
      color: 'var(--dns-ui-success, #138a5b)',
    };
  }
  if (status === 'False') {
    return {
      icon: 'mdi:alert-circle-outline',
      color: 'var(--dns-ui-danger, #b42318)',
    };
  }
  return {
    icon: 'mdi:progress-clock',
    color: 'var(--dns-ui-warning, #b7791f)',
  };
}

function conditionDetail(condition: DnsCondition): string {
  const reason = condition.reason ? `Reason: ${condition.reason}` : 'Reason: -';
  return condition.message ? `${reason} - ${condition.message}` : reason;
}

export function relativeTime(value?: string) {
  if (!value) {
    return '';
  }
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) {
    return value;
  }
  const seconds = Math.max(0, Math.round((Date.now() - timestamp) / 1000));
  if (seconds < 60) {
    return `${seconds} sec ago`;
  }
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return `${minutes} min ago`;
  }
  const hours = Math.round(minutes / 60);
  if (hours < 24) {
    return `${hours} hr ago`;
  }
  const days = Math.round(hours / 24);
  return `${days} d ago`;
}

export function StatusText({ value }: { value?: string }) {
  return <Typography sx={{ color: ui.muted, fontSize: 14 }}>{value || '-'}</Typography>;
}

export function JsonBlock({ value }: { value: unknown }) {
  return (
    <Box
      component="pre"
      sx={{
        bgcolor: ui.fieldBg,
        border: 1,
        borderColor: ui.borderSoft,
        borderRadius: 1,
        color: ui.text,
        fontSize: 12,
        m: 0,
        maxHeight: 320,
        overflow: 'auto',
        p: 1.5,
        whiteSpace: 'pre-wrap',
      }}
    >
      {JSON.stringify(value ?? {}, null, jsonIndent)}
    </Box>
  );
}

export function toYaml(value: unknown) {
  return yaml.dump(value ?? {}, {
    indent: jsonIndent,
    lineWidth: -1,
    noRefs: true,
    sortKeys: false,
  });
}

function toYamlDocumentStream(values: unknown[]) {
  return values.map(value => toYaml(value).trimEnd()).join('\n---\n') + '\n';
}

const pluginManagedMetadataPrefixes = ['dns.appthrust.io/', 'route53.dns.appthrust.io/'];

function isObjectRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

function pluginManagedMetadataMap(value: unknown): Record<string, string> | undefined {
  if (!isObjectRecord(value)) {
    return undefined;
  }

  const entries = Object.entries(value).filter(
    ([key, item]) =>
      typeof item === 'string' &&
      pluginManagedMetadataPrefixes.some(prefix => key.startsWith(prefix))
  );

  return entries.length ? (Object.fromEntries(entries) as Record<string, string>) : undefined;
}

export function manifestPreviewValue(value: unknown) {
  if (Array.isArray(value)) {
    return value.map(item => manifestPreviewValue(item));
  }

  if (
    !isObjectRecord(value) ||
    typeof value.apiVersion !== 'string' ||
    typeof value.kind !== 'string'
  ) {
    return value;
  }

  const sourceMetadata = isObjectRecord(value.metadata) ? value.metadata : {};
  const metadata: Record<string, unknown> = {};
  if (typeof sourceMetadata.name === 'string') {
    metadata.name = sourceMetadata.name;
  }
  if (typeof sourceMetadata.namespace === 'string') {
    metadata.namespace = sourceMetadata.namespace;
  }

  const annotations = pluginManagedMetadataMap(sourceMetadata.annotations);
  if (annotations) {
    metadata.annotations = annotations;
  }
  const labels = pluginManagedMetadataMap(sourceMetadata.labels);
  if (labels) {
    metadata.labels = labels;
  }

  return {
    apiVersion: value.apiVersion,
    kind: value.kind,
    metadata,
    ...(value.kind === 'Secret' && typeof value.type === 'string' ? { type: value.type } : {}),
    ...(value.kind === 'Secret' && isObjectRecord(value.stringData)
      ? { stringData: value.stringData }
      : {}),
    ...(Object.prototype.hasOwnProperty.call(value, 'spec') ? { spec: value.spec } : {}),
  };
}

export function DnsTextField(props: React.ComponentProps<typeof TextField>) {
  return <TextField {...props} variant="outlined" />;
}

export function DnsFormControl(props: React.ComponentProps<typeof FormControl>) {
  return <FormControl {...props} variant="outlined" />;
}

export function FormFieldError({ children }: { children?: React.ReactNode }) {
  if (!children) {
    return null;
  }
  return (
    <Typography sx={{ color: ui.dangerText, fontSize: 12, lineHeight: 1.5 }}>
      {children}
    </Typography>
  );
}

export function readJSON(text: string, fallback: unknown, field: string) {
  const trimmed = text.trim();
  if (!trimmed) {
    return fallback;
  }
  try {
    return JSON.parse(trimmed);
  } catch (error) {
    throw new Error(`${field} is not valid JSON: ${(error as Error).message}`);
  }
}

export function parseLabelLines(text: string) {
  return text
    .split(/\r?\n/)
    .map(line => line.trim())
    .filter(Boolean)
    .reduce<Record<string, string>>((labels, line) => {
      const [key, ...rest] = line.split('=');
      const value = rest.join('=').trim();
      if (key?.trim() && value) {
        labels[key.trim()] = value;
      }
      return labels;
    }, {});
}

export function labelsToLines(labels?: Record<string, string>) {
  return Object.entries(labels ?? {})
    .map(([key, value]) => `${key}=${value}`)
    .join('\n');
}

export function parseNamespaceLabelsYaml(text: string) {
  const trimmed = text.trim();
  if (!trimmed) {
    return {};
  }
  const parsed = yaml.load(trimmed);
  if (parsed == null) {
    return {};
  }
  if (Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error('labels must be a YAML mapping of key: value pairs');
  }
  return Object.entries(parsed as Record<string, unknown>).reduce<Record<string, string>>(
    (labels, [key, value]) => {
      if (!key.trim()) {
        return labels;
      }
      if (value == null || Array.isArray(value) || typeof value === 'object') {
        throw new Error(`label ${key} must have a scalar value`);
      }
      labels[key.trim()] = String(value);
      return labels;
    },
    {}
  );
}

function yamlLabelValue(value: string) {
  if (!value) {
    return '""';
  }
  if (
    /^(?:true|false|yes|no|on|off|null|~|[-+]?(?:\d+|\d+\.\d+|\.\d+)(?:e[-+]?\d+)?|\.nan|\.inf|[-+]\.inf)$/i.test(
      value
    )
  ) {
    return JSON.stringify(value);
  }
  if (/^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/.test(value)) {
    return value;
  }
  return JSON.stringify(value);
}

export function namespaceLabelsYaml(labels?: Record<string, string>) {
  return Object.entries(labels ?? {})
    .map(([key, value]) => `${key}: ${yamlLabelValue(value)}`)
    .join('\n');
}

export function useNotice() {
  const platform = useDnsPlatform();
  const [notice, setNotice] = React.useState<Notice | null>(null);
  const showSuccess = React.useCallback(
    (message: string) => {
      platform.notifications.success(message);
      setNotice({ severity: 'success', message });
    },
    [platform]
  );
  const showError = React.useCallback(
    (error: unknown) => {
      const message = (error as Error).message || String(error);
      platform.notifications.error(message);
      setNotice({ severity: 'error', message });
    },
    [platform]
  );
  const snackbar = (
    <Snackbar open={!!notice} autoHideDuration={6000} onClose={() => setNotice(null)}>
      {notice ? <Alert severity={notice.severity}>{notice.message}</Alert> : undefined}
    </Snackbar>
  );
  return { showSuccess, showError, snackbar };
}

export function SummaryTile({
  label,
  value,
  tone,
  icon,
  sub,
}: {
  label: string;
  value: number;
  tone?: 'warning' | 'success';
  icon?: string;
  sub?: string;
}) {
  return (
    <Panel sx={{ minWidth: 180, p: 2 }}>
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 1.5 }}>
        <IconBadge icon={icon ?? 'mdi:dns'} />
        <Icon icon="mdi:chevron-right" color={ui.faint} width={16} />
      </Stack>
      <Typography
        sx={{
          color: tone === 'warning' ? ui.warningText : ui.text,
          fontSize: 28,
          fontWeight: 700,
          lineHeight: 1.15,
        }}
      >
        {value}
      </Typography>
      <Typography sx={{ color: ui.muted, fontSize: 14, mt: 0.5 }}>{label}</Typography>
      {sub ? <Typography sx={{ color: ui.faint, fontSize: 12, mt: 1 }}>{sub}</Typography> : null}
    </Panel>
  );
}

export function Panel({
  children,
  sx,
}: {
  children: React.ReactNode;
  sx?: Record<string, unknown>;
}) {
  return (
    <Paper
      variant="outlined"
      sx={{
        bgcolor: ui.panelBg,
        borderRadius: 1,
        color: ui.text,
        ...sx,
      }}
    >
      {children}
    </Paper>
  );
}

export function IconBadge({
  icon,
  tone = 'neutral',
}: {
  icon: string;
  tone?: 'neutral' | 'accent';
}) {
  return (
    <Box
      sx={{
        alignItems: 'center',
        bgcolor: tone === 'accent' ? selectedBg : ui.fieldBg,
        borderRadius: 1,
        color: tone === 'accent' ? ui.accent : ui.muted,
        display: 'inline-flex',
        height: 36,
        justifyContent: 'center',
        width: 36,
      }}
    >
      <Icon icon={icon} width={19} />
    </Box>
  );
}

export function ProviderBadge({ provider }: { provider: string }) {
  const icon = provider.includes('Cloudflare')
    ? 'simple-icons:cloudflare'
    : provider.includes('Google')
    ? 'simple-icons:googlecloud'
    : 'simple-icons:amazonaws';
  return (
    <Chip
      size="small"
      icon={<Icon icon={icon} />}
      label={provider}
      sx={{
        bgcolor: ui.fieldBg,
        border: 1,
        borderColor: ui.border,
        color: ui.text,
        fontSize: 12,
        fontWeight: 600,
        height: 24,
        '& .MuiChip-icon': { color: ui.muted },
      }}
    />
  );
}

export function providerForResource(resource?: KubeObjectInterface) {
  const spec =
    resource?.spec && typeof resource.spec === 'object'
      ? (resource.spec as { controllerName?: string; provider?: string | { name?: string; version?: string } })
      : {};
  const provider =
    typeof spec.provider === 'string'
      ? spec.provider
      : `${spec.provider?.name ?? ''}/${spec.provider?.version ?? ''}`;
  const value = `${resource?.apiVersion ?? ''} ${resource?.kind ?? ''} ${
    spec.controllerName ?? ''
  } ${
    provider
  }`.toLowerCase();
  if (value.includes('cloudflare')) {
    return 'Cloudflare';
  }
  if (value.includes('google')) {
    return 'Google Cloud DNS';
  }
  return 'AWS Route 53';
}

export function ageOf(resource: KubeObjectInterface) {
  const createdAt = resource.metadata?.creationTimestamp;
  if (!createdAt) {
    return '-';
  }
  const ms = Date.now() - new Date(createdAt).getTime();
  if (!Number.isFinite(ms) || ms < 0) {
    return '-';
  }
  const minutes = Math.floor(ms / 60000);
  if (minutes < 60) {
    return `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 48) {
    return `${hours}h`;
  }
  return `${Math.floor(hours / 24)}d`;
}

export function descriptionOf(resource: KubeObjectInterface) {
  return resource.metadata?.annotations?.['dns.appthrust.io/description'] ?? '';
}

export function openResourceYaml(resource: KubeObjectInterface) {
  getDnsPlatform().liveYaml.open(resource);
}

const yamlEditorResources = new Set([
  'dns.appthrust.io/v1alpha1/Zone',
  'dns.appthrust.io/v1alpha1/ZoneClass',
  'dns.appthrust.io/v1alpha1/RecordSet',
  'route53.dns.appthrust.io/v1alpha1/Route53Identity',
  'cloudflare.dns.appthrust.io/v1alpha1/CloudflareIdentity',
]);

export function resourceYamlPath(resource: KubeObjectInterface) {
  const apiVersion = resource.apiVersion ?? '';
  const resourceKey = `${apiVersion}/${resource.kind}`;
  return yamlEditorResources.has(resourceKey) ? getDnsPlatform().liveYaml.pathFor(resource) : '';
}

export function DesignLinkButton({
  children,
  onClick,
  href,
  disabled,
}: {
  children: React.ReactNode;
  onClick?: () => void;
  href?: string;
  disabled?: boolean;
}) {
  const linkProps = href && !disabled ? { href } : {};
  return (
    <Button
      {...linkProps}
      variant="text"
      disabled={disabled}
      onClick={disabled ? undefined : onClick}
      sx={{
        color: disabled ? ui.muted : ui.accent,
        fontSize: 14,
        fontWeight: 600,
        justifyContent: 'flex-start',
        minWidth: 0,
        p: 0,
        textAlign: 'left',
        textTransform: 'none',
        '&.Mui-disabled': { color: ui.muted },
        '&:hover': disabled
          ? {}
          : { bgcolor: 'transparent', color: 'primary.dark', textDecoration: 'underline' },
      }}
    >
      {children}
    </Button>
  );
}

export function EmptyState({
  title,
  body,
  action,
}: {
  title: string;
  body: string;
  action?: React.ReactNode;
}) {
  return (
    <Box
      sx={{ alignItems: 'center', display: 'flex', minHeight: 320, justifyContent: 'center', p: 4 }}
    >
      <Stack spacing={1.5} alignItems="center" textAlign="center">
        <Typography sx={{ color: ui.text, fontSize: 16, fontWeight: 600 }}>{title}</Typography>
        <Typography sx={{ color: ui.faint, fontSize: 14, lineHeight: 1.65, maxWidth: 460 }}>
          {body}
        </Typography>
        {action ? <Box sx={{ pt: 1 }}>{action}</Box> : null}
      </Stack>
    </Box>
  );
}

export function DataGridTable({
  columns,
  children,
  framed = true,
}: {
  columns: string;
  children: React.ReactNode;
  framed?: boolean;
}) {
  const childRows = React.Children.toArray(children);
  const content = (
    <Box sx={{ '--dns-grid-columns': columns }}>
      {childRows.map((child, index) => (
        <React.Fragment key={index}>{child}</React.Fragment>
      ))}
    </Box>
  );
  return framed ? <Panel sx={{ overflow: 'hidden' }}>{content}</Panel> : content;
}

export function GridHeader({ labels }: { labels: Array<{ label: string; hideSmall?: boolean }> }) {
  return (
    <Box
      sx={{
        borderBottom: 1,
        borderColor: ui.border,
        display: 'grid',
        gridTemplateColumns: 'var(--dns-grid-columns)',
        px: 0.5,
        py: 0.75,
      }}
    >
      {labels.map(item => (
        <Box
          key={item.label}
          sx={{
            ...tableCellSx,
            ...(item.hideSmall ? { display: { xs: 'none', md: 'flex' } } : {}),
          }}
        >
          <Typography sx={tableHeaderSx}>{item.label}</Typography>
        </Box>
      ))}
    </Box>
  );
}

export function GridRow({ children }: { children: React.ReactNode }) {
  return (
    <Box
      sx={{
        borderBottom: 1,
        borderColor: ui.borderSoft,
        display: 'grid',
        gridTemplateColumns: 'var(--dns-grid-columns)',
        transition: 'background-color 120ms ease',
        '&:last-child': { borderBottom: 0 },
        '&:hover': { bgcolor: ui.rowHover },
      }}
    >
      {children}
    </Box>
  );
}

export function GridCell({
  children,
  hideSmall,
  sx,
}: {
  children: React.ReactNode;
  hideSmall?: boolean;
  sx?: Record<string, unknown>;
}) {
  return (
    <Box
      sx={{ ...tableCellSx, ...(hideSmall ? { display: { xs: 'none', md: 'flex' } } : {}), ...sx }}
    >
      {children}
    </Box>
  );
}

export function DetailFieldGrid({ fields }: { fields: Array<[string, React.ReactNode]> }) {
  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' } }}>
        {fields.map(([label, value], index) => (
          <Box
            key={label}
            sx={{
              borderBottom: 1,
              borderRight: { md: index % 2 === 0 ? 1 : 0 },
              borderBottomColor: ui.borderSoft,
              borderRightColor: ui.borderSoft,
              px: 2.5,
              py: 2,
            }}
          >
            <Typography sx={tableHeaderSx}>{label}</Typography>
            <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mt: 1 }}>
              {value || '-'}
            </Typography>
          </Box>
        ))}
      </Box>
    </Panel>
  );
}

export function DangerPanel({
  blocked,
  blockedMessage,
  readyMessage,
  actionLabel,
  onAction,
}: {
  blocked: boolean;
  blockedMessage: string;
  readyMessage: string;
  actionLabel: string;
  onAction: () => void;
}) {
  return (
    <Panel sx={{ bgcolor: ui.dangerBg, borderColor: ui.dangerBorder }}>
      <Stack
        direction={{ xs: 'column', md: 'row' }}
        justifyContent="space-between"
        alignItems={{ xs: 'stretch', md: 'flex-start' }}
        spacing={2}
        sx={{ p: 2.5 }}
      >
        <Stack direction="row" spacing={1.5}>
          <Icon icon="mdi:alert-outline" color={ui.dangerText} width={20} />
          <Box>
            <Typography sx={{ color: ui.dangerText, fontSize: 14, fontWeight: 600 }}>
              {blocked ? 'This resource cannot be deleted yet.' : 'Delete this resource'}
            </Typography>
            <Typography sx={{ color: ui.dangerText, fontSize: 14, lineHeight: 1.65, mt: 1 }}>
              {blocked ? blockedMessage : readyMessage}
            </Typography>
          </Box>
        </Stack>
        <ToolbarButton
          label={actionLabel}
          icon="mdi:delete"
          tone="danger"
          disabled={blocked}
          onClick={onAction}
        />
      </Stack>
    </Panel>
  );
}

export function FormPanel({
  title,
  children,
  footer,
}: {
  title?: string;
  children: React.ReactNode;
  footer?: React.ReactNode;
}) {
  return (
    <Panel sx={{ maxWidth: 960, overflow: 'hidden' }}>
      {title ? (
        <Box sx={{ borderBottom: 1, borderColor: ui.border, px: 2.5, py: 2 }}>
          <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>{title}</Typography>
        </Box>
      ) : null}
      <Stack spacing={2.5} sx={{ p: 2.5 }}>
        {children}
      </Stack>
      {footer ? (
        <Stack
          direction="row"
          justifyContent="flex-end"
          spacing={1}
          sx={{ borderTop: 1, borderColor: ui.border, px: 2.5, py: 2 }}
        >
          {footer}
        </Stack>
      ) : null}
    </Panel>
  );
}

export function FixedValue({ value }: { value?: React.ReactNode }) {
  return (
    <Box
      sx={{
        bgcolor: ui.fieldBg,
        border: 1,
        borderColor: ui.border,
        borderRadius: 1,
        color: ui.text,
        minHeight: 40,
        px: 1.5,
        py: 1,
      }}
    >
      <Typography sx={{ fontSize: 14 }}>{value || '-'}</Typography>
    </Box>
  );
}

export function ChoiceCard({
  selected,
  title,
  titlePrefix,
  description,
  disabled,
  children,
  onClick,
}: {
  selected?: boolean;
  title: string;
  titlePrefix?: React.ReactNode;
  description?: string;
  disabled?: boolean;
  children?: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <Button
      variant="outlined"
      disabled={disabled}
      onClick={onClick}
      sx={{
        alignItems: 'stretch',
        borderColor: selected ? ui.accent : ui.border,
        borderRadius: 1,
        bgcolor: selected ? selectedBg : ui.panelBg,
        color: ui.text,
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'flex-start',
        minHeight: 104,
        p: 2,
        textAlign: 'left',
        textTransform: 'none',
        '&:hover': { bgcolor: selected ? selectedHoverBg : ui.fieldBg, borderColor: ui.accent },
      }}
    >
      <Box sx={{ alignItems: 'center', columnGap: '8px', display: 'flex', minWidth: 0 }}>
        {titlePrefix}
        <Typography sx={{ fontSize: 14, fontWeight: 600, minWidth: 0 }}>{title}</Typography>
      </Box>
      {description ? (
        <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.55, mt: 1 }}>
          {description}
        </Typography>
      ) : null}
      {children ? <Box sx={{ mt: 1.5 }}>{children}</Box> : null}
    </Button>
  );
}

export function YamlPreview({
  value,
  complete = true,
  messages = [],
}: {
  value: unknown;
  complete?: boolean;
  messages?: string[];
}) {
  const previewError =
    isObjectRecord(value) && typeof value.error === 'string' ? value.error : undefined;
  const effectiveMessages = previewError ? unique([...messages, previewError]) : messages;
  const manifestValue = React.useMemo(
    () => manifestPreviewValue(previewError ? {} : value),
    [previewError, value]
  );
  const code = React.useMemo(
    () => (Array.isArray(manifestValue) ? toYamlDocumentStream(manifestValue) : toYaml(manifestValue)),
    [manifestValue]
  );

  return (
    <Accordion variant="outlined" disableGutters defaultExpanded>
      <AccordionSummary expandIcon={<Icon icon="mdi:chevron-down" />}>
        <Stack direction="row" spacing={1} alignItems="center" useFlexGap flexWrap="wrap">
          <Typography sx={{ fontSize: 14, fontWeight: 600 }}>Manifest preview</Typography>
          <Chip
            size="small"
            label={complete && !previewError ? 'Complete' : 'Incomplete'}
            sx={{
              bgcolor: complete && !previewError ? ui.successBg : ui.warningBgSoft,
              color: complete && !previewError ? ui.successText : ui.warningText,
              fontSize: 11,
              fontWeight: 700,
              height: 22,
            }}
          />
        </Stack>
      </AccordionSummary>
      <AccordionDetails sx={{ p: 0 }}>
        {effectiveMessages.length ? (
          <Box sx={{ borderBottom: theme => `1px solid ${theme.palette.divider}`, p: 1.5 }}>
            <Stack spacing={0.75}>
              {effectiveMessages.map((message, index) => (
                <Typography key={index} sx={{ color: ui.warningText, fontSize: 12 }}>
                  {message}
                </Typography>
              ))}
            </Stack>
          </Box>
        ) : null}
        <YamlCodeBlock code={code} />
      </AccordionDetails>
    </Accordion>
  );
}

export function IdentitySummaryCard({ identity }: { identity: ProviderIdentity }) {
  const provider = providerForResource(identity);
  const isCloudflare = identity.kind === 'CloudflareIdentity';
  return (
    <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
      <Stack spacing={1.25}>
        <Stack direction="row" spacing={1} alignItems="center" useFlexGap flexWrap="wrap">
          <ProviderBadge provider={provider} />
          <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
            {nameOf(identity)}
          </Typography>
          <Typography sx={{ color: ui.faint, fontSize: 13 }}>{namespaceOf(identity)}</Typography>
        </Stack>
        <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.55 }}>
          {descriptionOf(identity) || `${provider} Provider Identity.`}
        </Typography>
        <Box sx={{ display: 'grid', gap: 1.5, gridTemplateColumns: { xs: '1fr', sm: '1fr 1fr' } }}>
          <Box>
            <Typography sx={tableHeaderSx}>Kind</Typography>
            <Typography sx={{ color: ui.text, fontSize: 13 }}>{identity.kind}</Typography>
          </Box>
          <Box>
            <Typography sx={tableHeaderSx}>
              {isCloudflare ? 'Cloudflare account ID' : 'AWS account ID'}
            </Typography>
            <Typography sx={{ color: ui.text, fontSize: 13 }}>
              {isCloudflare ? identity.status?.account?.id ?? '-' : identity.spec.accountID}
            </Typography>
          </Box>
          {!isCloudflare ? (
            <Box>
              <Typography sx={tableHeaderSx}>AWS region</Typography>
              <Typography sx={{ color: ui.text, fontSize: 13 }}>{identity.spec.region}</Typography>
            </Box>
          ) : null}
        </Box>
        <Conditions conditions={identity.status?.conditions} />
      </Stack>
    </Panel>
  );
}

export function ZoneClassSummaryCard({ zoneClass }: { zoneClass: ZoneClass }) {
  return (
    <Panel sx={{ bgcolor: ui.panelBgSoft, p: 2 }}>
      <Stack spacing={1.25}>
        <Stack direction="row" spacing={1} alignItems="center" useFlexGap flexWrap="wrap">
          <ProviderBadge provider={providerForResource(zoneClass)} />
          <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>
            {namespaceOf(zoneClass)}/{nameOf(zoneClass)}
          </Typography>
        </Stack>
        <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.55 }}>
          {descriptionOf(zoneClass) ||
            'Public DNS only. Private DNS and VPC selection are not part of this flow.'}
        </Typography>
        <Conditions conditions={zoneClass.status?.conditions} />
      </Stack>
    </Panel>
  );
}

export type LabelSelector = {
  matchLabels?: Record<string, string>;
  matchExpressions?: Array<Record<string, unknown>>;
};

export function isEmptyNamespaceSelector(selector: LabelSelector) {
  return (
    Object.keys(selector.matchLabels ?? {}).length === 0 &&
    (selector.matchExpressions?.length ?? 0) === 0
  );
}

export function labelSelectorMatches(selector: LabelSelector, labels: Record<string, string>) {
  for (const [key, value] of Object.entries(selector.matchLabels ?? {})) {
    if (labels[key] !== value) {
      return false;
    }
  }
  for (const expression of selector.matchExpressions ?? []) {
    const key = String(expression.key ?? '');
    const operator = String(expression.operator ?? '');
    const values = Array.isArray(expression.values) ? expression.values.map(String) : [];
    const labelValue = labels[key];
    if (!key) {
      return false;
    }
    if (operator === 'In' && (labelValue === undefined || !values.includes(labelValue))) {
      return false;
    }
    if (operator === 'NotIn' && labelValue !== undefined && values.includes(labelValue)) {
      return false;
    }
    if (operator === 'Exists' && !(key in labels)) {
      return false;
    }
    if (operator === 'DoesNotExist' && key in labels) {
      return false;
    }
    if (!['In', 'NotIn', 'Exists', 'DoesNotExist'].includes(operator)) {
      return false;
    }
  }
  return true;
}

export function EventsTable({ events, error }: { events: KubeEvent[]; error?: unknown }) {
  return (
    <Panel sx={{ overflow: 'hidden' }}>
      <Box sx={{ borderBottom: 1, borderColor: ui.border, px: 2, py: 1.5 }}>
        <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600 }}>Events</Typography>
      </Box>
      {error ? (
        <Box sx={{ px: 2, py: 2 }}>
          <Typography sx={{ color: ui.faint, fontSize: 14 }}>
            Events could not be loaded. Check permission to list Event resources.
          </Typography>
          <Typography sx={{ color: ui.faint, fontSize: 12, lineHeight: 1.6, mt: 0.75 }}>
            {errorMessage(error)}
          </Typography>
        </Box>
      ) : events.length ? (
        <DataGridTable
          columns="minmax(90px,0.4fr) minmax(150px,0.65fr) minmax(280px,1fr) minmax(170px,0.7fr) minmax(80px,0.35fr)"
          framed={false}
        >
          <GridHeader
            labels={[
              { label: 'Type' },
              { label: 'Reason' },
              { label: 'Message' },
              { label: 'Last seen' },
              { label: 'Count' },
            ]}
          />
          {events.map(event => (
            <GridRow key={resourceKey(event)}>
              <GridCell>
                <EventTypeBadge type={event.type} />
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.text, fontSize: 14 }}>{event.reason || '-'}</Typography>
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.faint, fontSize: 14 }}>
                  {event.message || '-'}
                </Typography>
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.faint, fontSize: 14 }}>
                  {eventLastSeen(event) || '-'}
                </Typography>
              </GridCell>
              <GridCell>
                <Typography sx={{ color: ui.faint, fontSize: 14 }}>{eventCount(event)}</Typography>
              </GridCell>
            </GridRow>
          ))}
        </DataGridTable>
      ) : (
        <Box sx={{ px: 2, py: 2 }}>
          <Typography sx={{ color: ui.faint, fontSize: 14 }}>
            No events recorded for this resource.
          </Typography>
        </Box>
      )}
    </Panel>
  );
}

export function ResourceEventsPanel({ events, error }: { events: KubeEvent[]; error?: unknown }) {
  return (
    <Panel sx={{ p: 2.25 }}>
      <Typography sx={{ color: ui.text, fontSize: 14, fontWeight: 600, mb: 1.5 }}>
        Events
      </Typography>
      {error ? (
        <Typography sx={{ color: ui.faint, fontSize: 13, lineHeight: 1.5 }}>
          Events could not be loaded. Check permission to list Event resources.
        </Typography>
      ) : events.length ? (
        <Stack spacing={0}>
          {events.map((event, index) => (
            <Box
              key={resourceKey(event)}
              sx={{
                display: 'grid',
                gridTemplateColumns: '14px 1fr',
                pb: index === events.length - 1 ? 0 : 1.5,
                position: 'relative',
              }}
            >
              <Box sx={{ position: 'relative', pt: 0.5 }}>
                <Box
                  sx={{
                    bgcolor: event.type === 'Warning' ? 'danger' : 'success',
                    borderRadius: 999,
                    height: 8,
                    width: 8,
                  }}
                />
                {index === events.length - 1 ? null : (
                  <Box
                    sx={{
                      bgcolor: ui.borderSoft,
                      bottom: -1,
                      left: 3.5,
                      position: 'absolute',
                      top: 14,
                      width: 1,
                    }}
                  />
                )}
              </Box>
              <Box sx={{ minWidth: 0 }}>
                <Typography
                  sx={{ color: ui.text, fontSize: 13, fontWeight: 700, lineHeight: 1.4 }}
                >
                  {event.reason || '-'} - {event.message || '-'}
                </Typography>
                <Stack direction="row" spacing={1} alignItems="center" sx={{ mt: 0.75 }}>
                  <Typography
                    sx={{
                      color: event.type === 'Warning' ? ui.dangerText : ui.successText,
                      fontSize: 12,
                      fontWeight: 700,
                      lineHeight: 1.6,
                    }}
                  >
                    {event.type || '-'}
                  </Typography>
                  <Typography
                    sx={{ color: ui.faint, fontFamily: 'monospace', fontSize: 12, lineHeight: 1.6 }}
                  >
                    {eventLastSeen(event) || '-'} · x{eventCount(event)}
                  </Typography>
                </Stack>
              </Box>
            </Box>
          ))}
        </Stack>
      ) : (
        <Typography sx={{ color: ui.faint, fontSize: 13 }}>
          No events recorded for this resource.
        </Typography>
      )}
    </Panel>
  );
}

function EventTypeBadge({ type }: { type?: string }) {
  const eventType = type || '-';
  const warning = eventType === 'Warning';
  return <StatusBadge label={eventType} tone={warning ? 'danger' : 'success'} />;
}

function eventLastSeen(event: KubeEvent) {
  return (
    event.series?.lastObservedTime ||
    event.lastTimestamp ||
    event.eventTime ||
    event.metadata?.creationTimestamp ||
    ''
  );
}

function eventCount(event: KubeEvent) {
  return event.series?.count ?? event.count ?? 1;
}

function eventSortTime(event: KubeEvent) {
  const time = Date.parse(eventLastSeen(event));
  return Number.isFinite(time) ? time : 0;
}

export function unique(values: string[]) {
  return Array.from(new Set(values.filter(Boolean))).sort();
}

export function eventsForResource(events: KubeEvent[], resource: KubeObjectInterface) {
  return events
    .filter(event => {
      const involved = event.involvedObject;
      const sameUID =
        involved?.uid && resource.metadata?.uid ? involved.uid === resource.metadata.uid : true;
      const sameAPI =
        involved?.apiVersion && resource.apiVersion
          ? involved.apiVersion === resource.apiVersion
          : true;
      return (
        sameUID &&
        sameAPI &&
        involved?.kind === resource.kind &&
        involved?.name === nameOf(resource) &&
        (involved.namespace || '') === namespaceOf(resource)
      );
    })
    .sort((left, right) => eventSortTime(right) - eventSortTime(left));
}

export function errorMessage(error: unknown) {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}
