/** @jsxRuntime classic */
import React from 'react';

export type DnsTheme = {
  color: {
    surface: string;
    surfaceMuted: string;
    border: string;
    text: string;
    textMuted: string;
    accent: string;
    onAccent: string;
    danger: string;
    warning: string;
    success: string;
  };
  radius: {
    sm: string;
    md: string;
  };
  focusRing: string;
  fontFamily: string;
  density: {
    spacingUnit: string;
    controlHeight: string;
  };
};

export const defaultDnsTheme: DnsTheme = {
  color: {
    surface: '#ffffff',
    surfaceMuted: '#f6f8fb',
    border: '#d8dee8',
    text: '#172033',
    textMuted: '#667085',
    accent: '#2563eb',
    onAccent: '#ffffff',
    danger: '#d92d20',
    warning: '#b7791f',
    success: '#138a5b',
  },
  radius: {
    sm: '4px',
    md: '8px',
  },
  focusRing: '0 0 0 3px color-mix(in srgb, #2563eb 28%, transparent)',
  fontFamily:
    'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
  density: {
    spacingUnit: '8px',
    controlHeight: '38px',
  },
};

function cssVariablesForTheme(theme: DnsTheme): Record<string, string> {
  return {
    '--dns-ui-surface': theme.color.surface,
    '--dns-ui-surface-muted': theme.color.surfaceMuted,
    '--dns-ui-border': theme.color.border,
    '--dns-ui-text': theme.color.text,
    '--dns-ui-text-muted': theme.color.textMuted,
    '--dns-ui-accent': theme.color.accent,
    '--dns-ui-on-accent': theme.color.onAccent,
    '--dns-ui-danger': theme.color.danger,
    '--dns-ui-warning': theme.color.warning,
    '--dns-ui-success': theme.color.success,
    '--dns-ui-radius-sm': theme.radius.sm,
    '--dns-ui-radius-md': theme.radius.md,
    '--dns-ui-focus-ring': theme.focusRing,
    '--dns-ui-font-family': theme.fontFamily,
    '--dns-ui-spacing-unit': theme.density.spacingUnit,
    '--dns-ui-control-height': theme.density.controlHeight,
  };
}

function useDocumentThemeVariables(variables: Record<string, string>) {
  const serializedVariables = JSON.stringify(variables);
  const useIsomorphicLayoutEffect =
    typeof window === 'undefined' ? React.useEffect : React.useLayoutEffect;

  useIsomorphicLayoutEffect(() => {
    if (typeof document === 'undefined') {
      return undefined;
    }

    const root = document.documentElement;
    const previousValues = new Map<string, string>();
    const nextVariables = JSON.parse(serializedVariables) as Record<string, string>;

    for (const [name, value] of Object.entries(nextVariables)) {
      previousValues.set(name, root.style.getPropertyValue(name));
      root.style.setProperty(name, value);
    }

    return () => {
      for (const [name, previousValue] of previousValues.entries()) {
        if (previousValue) {
          root.style.setProperty(name, previousValue);
        } else {
          root.style.removeProperty(name);
        }
      }
    };
  }, [serializedVariables]);
}

export function DnsThemeProvider({
  theme = defaultDnsTheme,
  children,
}: {
  theme?: DnsTheme;
  children: React.ReactNode;
}) {
  const themeVariables = React.useMemo(() => cssVariablesForTheme(theme), [theme]);
  useDocumentThemeVariables(themeVariables);

  const style = {
    ...themeVariables,
    background: theme.color.surfaceMuted,
    color: theme.color.text,
    fontFamily: theme.fontFamily,
  } as React.CSSProperties;

  return <div style={style}>{children}</div>;
}
