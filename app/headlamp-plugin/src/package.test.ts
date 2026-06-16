import { beforeAll, describe, expect, it, vi } from 'vitest';
import { sidebarEntries } from './sidebarEntries';
import { createHeadlampTheme } from './theme';

describe('headlamp-plugin-dns-api', () => {
  beforeAll(() => {
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: () => undefined,
      removeItem: () => undefined,
      clear: () => undefined,
    });
  });

  it('has a test target for plugin tooling', () => {
    expect(true).toBe(true);
  });

  it('keeps semantic status colors visible in dark mode', () => {
    const theme = createHeadlampTheme({
      palette: {
        mode: 'dark',
        success: { main: '#ffffff' },
        warning: { main: '#ffffff' },
        error: { main: '#ffffff' },
      },
    } as never);

    expect(theme.color.success).toBe('#54d87f');
    expect(theme.color.warning).toBe('#f4b84f');
    expect(theme.color.danger).toBe('#ff8a8a');
  });

  it('does not register parent and child sidebar entries with the same URL', () => {
    const urlsByName = new Map(
      sidebarEntries.filter(entry => entry.url).map(entry => [entry.name, entry.url])
    );

    for (const entry of sidebarEntries) {
      if (!entry.parent || !entry.url) {
        continue;
      }

      expect(entry.url, `${entry.name} must not reuse its parent URL`).not.toBe(
        urlsByName.get(entry.parent)
      );
    }
  });
});
