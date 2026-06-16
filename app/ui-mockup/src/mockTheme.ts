import type { DnsTheme } from '@appthrust/dns-api-ui';

export const mockTheme: DnsTheme = {
  color: {
    surface: '#ffffff',
    surfaceMuted: '#f5f7fa',
    border: '#d7dde8',
    text: '#182230',
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
  focusRing: '0 0 0 3px rgba(37, 99, 235, 0.24)',
  fontFamily:
    'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
  density: {
    spacingUnit: '8px',
    controlHeight: '38px',
  },
};
