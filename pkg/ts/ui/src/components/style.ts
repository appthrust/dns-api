import type React from 'react';

export type Sx = Record<string, unknown> | undefined;
export type Breakpoint = 'xs' | 'sm' | 'md';

type AnyProps = Record<string, any>;

const tokenMap: Record<string, string> = {
  accent: 'var(--dns-ui-accent, #2563eb)',
  accentStrong: 'var(--dns-ui-accent, #2563eb)',
  border: 'var(--dns-ui-border, #d8dee8)',
  borderSoft: 'var(--dns-ui-border, #d8dee8)',
  danger: 'var(--dns-ui-danger, #d92d20)',
  dangerBorder: 'var(--dns-ui-danger, #d92d20)',
  dangerSurface: 'color-mix(in srgb, var(--dns-ui-danger, #d92d20) 12%, var(--dns-ui-surface, #ffffff))',
  dangerText: 'var(--dns-ui-danger, #d92d20)',
  fieldBg: 'var(--dns-ui-surface-muted, #f6f8fb)',
  focusRing: 'var(--dns-ui-focus-ring, 0 0 0 3px rgba(37, 99, 235, 0.24))',
  muted: 'var(--dns-ui-text-muted, #667085)',
  onStrong: 'var(--dns-ui-on-accent, #ffffff)',
  panelBg: 'var(--dns-ui-surface, #ffffff)',
  panelBgSoft: 'var(--dns-ui-surface-muted, #f6f8fb)',
  rowHover: 'var(--dns-ui-surface-muted, #f6f8fb)',
  selected: 'color-mix(in srgb, var(--dns-ui-accent, #2563eb) 12%, var(--dns-ui-surface, #ffffff))',
  selectedHover: 'color-mix(in srgb, var(--dns-ui-accent, #2563eb) 17%, var(--dns-ui-surface, #ffffff))',
  sidebarBg: 'var(--dns-ui-surface, #ffffff)',
  success: 'var(--dns-ui-success, #138a5b)',
  successSurface: 'color-mix(in srgb, var(--dns-ui-success, #138a5b) 12%, var(--dns-ui-surface, #ffffff))',
  surface: 'var(--dns-ui-surface, #ffffff)',
  surfaceMuted: 'var(--dns-ui-surface-muted, #f6f8fb)',
  text: 'var(--dns-ui-text, #172033)',
  textMuted: 'var(--dns-ui-text-muted, #667085)',
  warning: 'var(--dns-ui-warning, #b7791f)',
  warningSurface: 'color-mix(in srgb, var(--dns-ui-warning, #b7791f) 16%, var(--dns-ui-surface, #ffffff))',
};

const styleKeyMap: Record<string, string> = {
  bgcolor: 'backgroundColor',
  m: 'margin',
  mt: 'marginTop',
  mr: 'marginRight',
  mb: 'marginBottom',
  ml: 'marginLeft',
  mx: 'marginInline',
  my: 'marginBlock',
  p: 'padding',
  pt: 'paddingTop',
  pr: 'paddingRight',
  pb: 'paddingBottom',
  pl: 'paddingLeft',
  px: 'paddingInline',
  py: 'paddingBlock',
};

export function token(value: string) {
  return tokenMap[value] ?? value;
}

export function responsiveValue(value: unknown, breakpoint: Breakpoint = 'md'): unknown {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    const object = value as Record<string, unknown>;
    if (breakpoint === 'md') {
      return object.md ?? object.sm ?? object.xs ?? undefined;
    }
    if (breakpoint === 'sm') {
      return object.sm ?? object.xs ?? object.md ?? undefined;
    }
    return object.xs ?? object.sm ?? object.md ?? undefined;
  }
  return value;
}

function spacingValue(value: unknown, breakpoint: Breakpoint): string | number | undefined {
  const resolved = responsiveValue(value, breakpoint);
  if (typeof resolved === 'number') {
    return `calc(var(--dns-ui-spacing-unit, 8px) * ${resolved})`;
  }
  if (typeof resolved === 'string') {
    return token(resolved);
  }
  return undefined;
}

export function cssValue(value: unknown, breakpoint: Breakpoint = 'md'): unknown {
  const resolved = responsiveValue(value, breakpoint);
  if (typeof resolved === 'string') {
    return token(resolved);
  }
  if (typeof resolved === 'number') {
    return resolved;
  }
  return resolved;
}

export function sxToStyle(sx: Sx, breakpoint: Breakpoint = 'md'): React.CSSProperties {
  const style: React.CSSProperties = {};
  for (const [key, rawValue] of Object.entries(sx ?? {})) {
    if (key.startsWith('&') || key.startsWith('@')) {
      continue;
    }
    if (key === 'border' && rawValue === 1) {
      style.border = `1px solid ${token('border')}`;
      continue;
    }
    if (key === 'borderBottom' && rawValue === 1) {
      style.borderBottom = `1px solid ${token('border')}`;
      continue;
    }
    if (key === 'borderTop' && rawValue === 1) {
      style.borderTop = `1px solid ${token('border')}`;
      continue;
    }
    if (key === 'borderRight' && rawValue === 1) {
      style.borderRight = `1px solid ${token('border')}`;
      continue;
    }
    if (key === 'borderRadius' && typeof rawValue === 'number') {
      style.borderRadius = rawValue <= 1 ? 'var(--dns-ui-radius-sm, 4px)' : `calc(${rawValue} * var(--dns-ui-radius-sm, 4px))`;
      continue;
    }
    const styleKey = styleKeyMap[key] ?? key;
    const value = /^[mp][trblxy]?$/.test(key)
      ? spacingValue(rawValue, breakpoint)
      : cssValue(rawValue, breakpoint);
    if (value !== undefined && typeof value !== 'object') {
      (style as AnyProps)[styleKey] = value;
    }
  }
  return style;
}

export function elementType(component: AnyProps['component'], fallback: React.ElementType) {
  return component ?? fallback;
}
