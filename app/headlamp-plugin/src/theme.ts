import type { DnsTheme } from '@appthrust/dns-api-ui';
import { type Theme, useTheme } from '@mui/material/styles';

function themeValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value : fallback;
}

function firstThemeValue(fallback: string, ...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) {
      return value;
    }
  }
  return fallback;
}

function radiusValue(value: unknown, fallback: string): string {
  return typeof value === 'number' ? `${value}px` : themeValue(value, fallback);
}

export function createHeadlampTheme(headlampTheme?: Theme): DnsTheme {
  const palette = headlampTheme?.palette;
  const isDark = palette?.mode === 'dark';
  const accent = themeValue(palette?.primary?.main, isDark ? '#4B99EE' : '#2563eb');
  const danger = firstThemeValue(
    isDark ? '#ef6f73' : '#d92d20',
    isDark ? palette?.error?.light : undefined,
    palette?.error?.main
  );
  const background = palette?.background as Theme['palette']['background'] & {
    muted?: string;
  };

  return {
    color: {
      surface: themeValue(background?.paper, isDark ? '#1f1f1f' : '#ffffff'),
      surfaceMuted: themeValue(background?.muted, isDark ? '#1B1A19' : '#f6f8fb'),
      border: themeValue(palette?.divider, isDark ? 'rgba(255,255,255,0.12)' : '#d8dee8'),
      text: themeValue(palette?.text?.primary, isDark ? '#ffffff' : '#172033'),
      textMuted: themeValue(
        palette?.text?.secondary,
        isDark ? 'rgba(255,255,255,0.72)' : '#667085'
      ),
      accent,
      onAccent: themeValue(palette?.primary?.contrastText, isDark ? '#172033' : '#ffffff'),
      danger: isDark ? '#ff8a8a' : danger,
      warning: isDark ? '#f4b84f' : themeValue(palette?.warning?.main, '#b7791f'),
      success: isDark ? '#54d87f' : themeValue(palette?.success?.main, '#138a5b'),
    },
    radius: {
      sm: radiusValue(headlampTheme?.shape?.borderRadius, '4px'),
      md: radiusValue(headlampTheme?.shape?.borderRadius, '8px'),
    },
    focusRing: `0 0 0 3px color-mix(in srgb, ${accent} 28%, transparent)`,
    fontFamily: themeValue(
      headlampTheme?.typography?.fontFamily,
      'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif'
    ),
    density: {
      spacingUnit: '8px',
      controlHeight: '38px',
    },
  };
}

export function useHeadlampTheme(): DnsTheme {
  return createHeadlampTheme(useTheme());
}
