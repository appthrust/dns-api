/** @jsxRuntime classic */
import React from 'react';
import { clsx } from 'clsx';
import { Accordion, AccordionDetails, AccordionSummary } from './Accordion';
import { Dialog, DialogActions, DialogContent, DialogTitle } from './Dialog';
import { Select, MenuItem } from './Select';
import { Tooltip } from './Tooltip';
import {
  cssValue,
  elementType,
  responsiveValue,
  sxToStyle,
  token,
  type Breakpoint,
  type Sx,
} from './style';

type AnyProps = Record<string, any>;

function currentBreakpoint(): Breakpoint {
  if (typeof window === 'undefined') {
    return 'md';
  }
  if (window.matchMedia('(min-width: 900px)').matches) {
    return 'md';
  }
  if (window.matchMedia('(min-width: 600px)').matches) {
    return 'sm';
  }
  return 'xs';
}

function useBreakpoint(): Breakpoint {
  const [breakpoint, setBreakpoint] = React.useState<Breakpoint>(() => currentBreakpoint());

  React.useEffect(() => {
    const mediaQueries = [
      window.matchMedia('(min-width: 600px)'),
      window.matchMedia('(min-width: 900px)'),
    ];
    const update = () => setBreakpoint(currentBreakpoint());
    mediaQueries.forEach(query => query.addEventListener('change', update));
    update();
    return () => {
      mediaQueries.forEach(query => query.removeEventListener('change', update));
    };
  }, []);

  return breakpoint;
}

export { Accordion, AccordionDetails, AccordionSummary };
export { Dialog, DialogActions, DialogContent, DialogTitle };
export { MenuItem, Select };
export { Tooltip };

export function Box({ component, sx, style, children, className, ...props }: AnyProps) {
  const breakpoint = useBreakpoint();
  const Component = elementType(component, 'div');
  return (
    <Component
      {...props}
      className={clsx(className)}
      style={{ ...sxToStyle(sx, breakpoint), ...style }}
    >
      {children}
    </Component>
  );
}

export function Stack({
  alignItems,
  direction = 'column',
  flexWrap,
  justifyContent,
  spacing = 0,
  sx,
  style,
  textAlign,
  useFlexGap: _useFlexGap,
  children,
  className,
  ...props
}: AnyProps) {
  const breakpoint = useBreakpoint();
  const resolvedSpacing = responsiveValue(spacing, breakpoint);
  return (
    <div
      {...props}
      className={clsx(className)}
      style={{
        alignItems: cssValue(alignItems, breakpoint) as React.CSSProperties['alignItems'],
        display: 'flex',
        flexDirection: responsiveValue(direction, breakpoint) as React.CSSProperties['flexDirection'],
        flexWrap: cssValue(flexWrap, breakpoint) as React.CSSProperties['flexWrap'],
        gap:
          typeof resolvedSpacing === 'number'
            ? `calc(var(--dns-ui-spacing-unit, 8px) * ${resolvedSpacing})`
            : (cssValue(spacing, breakpoint) as React.CSSProperties['gap']),
        justifyContent: cssValue(justifyContent, breakpoint) as React.CSSProperties['justifyContent'],
        textAlign: cssValue(textAlign, breakpoint) as React.CSSProperties['textAlign'],
        ...sxToStyle(sx, breakpoint),
        ...style,
      }}
    >
      {children}
    </div>
  );
}

export function Typography({ component, sx, style, children, className, noWrap, ...props }: AnyProps) {
  const breakpoint = useBreakpoint();
  const Component = elementType(component, 'div');
  return (
    <Component
      {...props}
      className={clsx(className)}
      style={{
        margin: 0,
        minWidth: noWrap ? 0 : undefined,
        overflow: noWrap ? 'hidden' : undefined,
        textOverflow: noWrap ? 'ellipsis' : undefined,
        whiteSpace: noWrap ? 'nowrap' : undefined,
        ...sxToStyle(sx, breakpoint),
        ...style,
      }}
    >
      {children}
    </Component>
  );
}

export function Button({
  component,
  startIcon,
  sx,
  style,
  children,
  variant = 'text',
  color,
  disabled,
  to,
  href,
  className,
  ...props
}: AnyProps) {
  const breakpoint = useBreakpoint();
  const Component = elementType(component, href || to ? 'a' : 'button');
  const isDanger = color === 'error';
  return (
    <Component
      {...props}
      aria-disabled={disabled || undefined}
      className={clsx(className)}
      disabled={Component === 'button' ? disabled : undefined}
      href={href ?? to}
      style={{
        alignItems: 'center',
        background: variant === 'contained' ? (isDanger ? token('danger') : token('accent')) : 'transparent',
        border:
          variant === 'outlined'
            ? `1px solid ${isDanger ? token('danger') : token('border')}`
            : '1px solid transparent',
        borderRadius: 'var(--dns-ui-radius-sm, 4px)',
        color: variant === 'contained' ? token('onStrong') : isDanger ? token('danger') : token('accent'),
        cursor: disabled ? 'not-allowed' : 'pointer',
        display: 'inline-flex',
        font: 'inherit',
        gap: 8,
        opacity: disabled ? 0.55 : 1,
        padding: '7px 12px',
        textDecoration: 'none',
        ...sxToStyle(sx, breakpoint),
        ...style,
      }}
    >
      {startIcon}
      {children}
    </Component>
  );
}

export function IconButton({ sx, style, children, disabled, className, ...props }: AnyProps) {
  const breakpoint = useBreakpoint();
  return (
    <button
      {...props}
      className={clsx(className)}
      disabled={disabled}
      style={{
        alignItems: 'center',
        background: 'transparent',
        border: '1px solid transparent',
        borderRadius: 'var(--dns-ui-radius-sm, 4px)',
        color: 'inherit',
        cursor: disabled ? 'not-allowed' : 'pointer',
        display: 'inline-flex',
        height: 32,
        justifyContent: 'center',
        opacity: disabled ? 0.55 : 1,
        padding: 4,
        width: 32,
        ...sxToStyle(sx, breakpoint),
        ...style,
      }}
    >
      {children}
    </button>
  );
}

export function Chip({ icon, label, sx, style, className }: AnyProps) {
  const breakpoint = useBreakpoint();
  return (
    <span
      className={clsx(className)}
      style={{
        alignItems: 'center',
        borderRadius: 999,
        display: 'inline-flex',
        gap: 6,
        padding: '2px 8px',
        ...sxToStyle(sx, breakpoint),
        ...style,
      }}
    >
      {icon}
      {label}
    </span>
  );
}

export function Paper({ component, variant, sx, style, children, className, ...props }: AnyProps) {
  const breakpoint = useBreakpoint();
  const Component = elementType(component, 'div');
  return (
    <Component
      {...props}
      className={clsx(className)}
      data-variant={variant}
      style={{
        background: token('surface'),
        border: variant === 'outlined' ? `1px solid ${token('border')}` : undefined,
        borderRadius: 'var(--dns-ui-radius-md, 8px)',
        ...sxToStyle(sx, breakpoint),
        ...style,
      }}
    >
      {children}
    </Component>
  );
}

export function Snackbar({ open, children }: AnyProps) {
  if (!open) {
    return null;
  }
  return <div style={{ bottom: 16, position: 'fixed', right: 16, zIndex: 80 }}>{children}</div>;
}

export function Alert({ severity = 'info', sx, style, children }: AnyProps) {
  const breakpoint = useBreakpoint();
  const surface = severity === 'error' ? 'dangerSurface' : severity === 'success' ? 'successSurface' : severity === 'warning' ? 'warningSurface' : 'surfaceMuted';
  const accent = severity === 'error' ? 'danger' : severity === 'success' ? 'success' : severity === 'warning' ? 'warning' : 'accent';
  return (
    <div
      role="status"
      data-severity={severity}
      style={{
        background: token(surface),
        border: `1px solid ${token(accent)}`,
        borderRadius: 'var(--dns-ui-radius-md, 8px)',
        color: token('text'),
        padding: '10px 12px',
        ...sxToStyle(sx, breakpoint),
        ...style,
      }}
    >
      {children}
    </div>
  );
}

export function TextField({
  label,
  helperText,
  error,
  multiline,
  minRows,
  sx,
  style,
  fullWidth: _fullWidth,
  inputProps,
  ...props
}: AnyProps) {
  const breakpoint = useBreakpoint();
  const Input = multiline ? 'textarea' : 'input';
  const Wrapper = label ? 'label' : 'div';
  return (
    <Wrapper style={{ display: 'grid', gap: 6, ...sxToStyle(sx, breakpoint), ...style }}>
      {label ? <span style={{ color: token('textMuted'), fontSize: 13 }}>{label}</span> : null}
      <Input
        {...inputProps}
        {...props}
        rows={multiline ? minRows : undefined}
        type={!multiline && !props.type ? 'text' : props.type}
        style={{
          background: token('fieldBg'),
          border: `1px solid ${error ? token('danger') : token('border')}`,
          borderRadius: 'var(--dns-ui-radius-sm, 4px)',
          color: token('text'),
          font: 'inherit',
          minHeight: multiline ? undefined : 'var(--dns-ui-control-height, 38px)',
          padding: '8px 10px',
          resize: multiline ? 'vertical' : undefined,
          width: '100%',
        }}
      />
      {helperText ? (
        <span style={{ color: error ? token('danger') : token('textMuted'), fontSize: 12 }}>
          {helperText}
        </span>
      ) : null}
    </Wrapper>
  );
}

export function FormControl({ sx, style, children }: AnyProps) {
  const breakpoint = useBreakpoint();
  return (
    <div style={{ display: 'grid', gap: 6, ...sxToStyle(sx, breakpoint), ...style }}>
      {children}
    </div>
  );
}

export function InputLabel({ children }: { children: React.ReactNode }) {
  return <label style={{ color: token('textMuted'), fontSize: 13 }}>{children}</label>;
}

export function TableContainer({ component, children, ...props }: AnyProps) {
  const Component = elementType(component, 'div');
  return <Component {...props}>{children}</Component>;
}

export function Table({ children }: AnyProps) {
  return <table style={{ borderCollapse: 'collapse', width: '100%' }}>{children}</table>;
}

export function TableHead({ children }: AnyProps) {
  return <thead>{children}</thead>;
}

export function TableBody({ children }: AnyProps) {
  return <tbody>{children}</tbody>;
}

export function TableRow({ children }: AnyProps) {
  return <tr>{children}</tr>;
}

export function TableCell({ children }: AnyProps) {
  return <td style={{ borderTop: `1px solid ${token('border')}`, padding: '8px 10px' }}>{children}</td>;
}
