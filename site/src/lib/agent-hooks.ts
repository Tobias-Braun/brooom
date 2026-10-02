/**
 * Stop hooks that run brooom when an agent finishes, one per harness. The
 * file names and shapes follow the vendor docs (checked 2026-10-02):
 *
 * - Claude Code: https://code.claude.com/docs/en/hooks
 * - Cursor: https://cursor.com/docs/agent/hooks
 * - Codex: https://learn.chatgpt.com/docs/hooks
 *
 * All three read a hook's stdout as JSON (Codex rejects plain text for Stop),
 * so brooom's report goes to stderr, where it lands in the hook log, and `{}`
 * on stdout means "no decision". A failed sweep keeps its exit code.
 * `--format plain` is not an option: it only reports and is rejected with
 * --yes.
 */

export const hookCommand = `br sweep after-agents --yes >&2 && echo '{}'`;

export interface AgentHook {
  id: string;
  label: string;
  /** File the snippet goes into, relative to the project root. */
  file: string;
  snippet: string;
}

function json(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

export const agentHooks: AgentHook[] = [
  {
    id: 'claude-code',
    label: 'Claude Code',
    file: '.claude/settings.json',
    snippet: json({ hooks: { Stop: [{ hooks: [{ type: 'command', command: hookCommand }] }] } }),
  },
  {
    id: 'cursor',
    label: 'Cursor',
    file: '.cursor/hooks.json',
    snippet: json({ version: 1, hooks: { stop: [{ command: hookCommand }] } }),
  },
  {
    id: 'codex',
    label: 'Codex',
    file: '.codex/hooks.json',
    snippet: json({ hooks: { Stop: [{ hooks: [{ type: 'command', command: hookCommand }] }] } }),
  },
];
