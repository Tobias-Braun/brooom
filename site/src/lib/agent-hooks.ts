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
 *
 * Agents also run in folders that are not git repositories, where a sweep
 * would fail after every turn, so the hook checks for a repository first and
 * otherwise just answers `{}`.
 */

export const hookCommand = `if git rev-parse --git-dir >/dev/null 2>&1; then br sweep after-agents --yes >&2 || exit; fi; echo '{}'`;

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
