import { describe, expect, it } from 'vitest';
import { installTabs } from './install-tabs';

describe('install tabs data', () => {
  it('lists the tabs in the documented order', () => {
    expect(installTabs.map((t) => t.label)).toEqual([
      'Install script',
      'Homebrew',
      'winget',
      'AUR',
      'deb / rpm',
    ]);
  });

  it('has unique ids and only the first two tabs available', () => {
    expect(new Set(installTabs.map((t) => t.id)).size).toBe(installTabs.length);
    expect(installTabs.map((t) => t.status)).toEqual([
      'available',
      'available',
      'soon',
      'soon',
      'soon',
    ]);
  });

  it('shows no commands for coming-soon tabs', () => {
    for (const tab of installTabs.filter((t) => t.status === 'soon')) {
      expect(tab.blocks.some((b) => b.type === 'command')).toBe(false);
    }
  });

  it('labels script commands by OS and copies them without prompt characters', () => {
    const script = installTabs.find((t) => t.id === 'script')!;
    const commands = script.blocks.flatMap((b) => (b.type === 'command' ? [b] : []));
    expect(commands.map((c) => c.os)).toEqual(['macOS / Linux', 'Windows (PowerShell)']);
    for (const c of commands) expect(c.command).not.toMatch(/^[$>#] /);
  });
});
