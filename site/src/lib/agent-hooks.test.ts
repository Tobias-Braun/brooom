import { describe, expect, it } from 'vitest';
import { agentHooks, hookCommand } from './agent-hooks';

describe('agent hooks data', () => {
  it('lists Claude Code, Cursor and Codex with unique ids', () => {
    expect(agentHooks.map((h) => h.label)).toEqual(['Claude Code', 'Cursor', 'Codex']);
    expect(new Set(agentHooks.map((h) => h.id)).size).toBe(agentHooks.length);
  });

  it('has valid JSON snippets that all run the shared command', () => {
    for (const hook of agentHooks) {
      expect(() => JSON.parse(hook.snippet)).not.toThrow();
      expect(JSON.stringify(JSON.parse(hook.snippet))).toContain(JSON.stringify(hookCommand));
    }
  });

  it('auto-accepts the after-agents preset and keeps stdout valid JSON', () => {
    expect(hookCommand).toBe(`br sweep after-agents --yes >&2 && echo '{}'`);
  });
});
