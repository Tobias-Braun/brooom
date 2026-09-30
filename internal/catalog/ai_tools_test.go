package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// embeddedCatalog loads the shipped data for goos.
func embeddedCatalog(t *testing.T, goos string) *Catalog {
	t.Helper()
	c, err := Load(Options{GOOS: goos})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// wantAITools are the tools issue #28 asks for; the test fails when one is
// dropped or renamed, because config toggles refer to these ids.
var wantAITools = []string{
	"claude-code", "cursor", "aider", "github-copilot", "codex-cli", "gemini-cli", "continue",
	"cline", "roo-code", "windsurf", "goose", "openhands", "amp", "zed", "agent-run-logs",
}

func TestAIToolsCatalogShape(t *testing.T) {
	c := embeddedCatalog(t, "linux")
	for _, id := range wantAITools {
		tool, ok := c.Tool(id)
		if !ok {
			t.Errorf("tool %q missing", id)
			continue
		}
		if tool.Category != CategoryAI {
			t.Errorf("tool %q category = %q, want ai", id, tool.Category)
		}
		if tool.Name == "" || len(tool.Entries) == 0 || len(tool.Protect) == 0 {
			t.Errorf("tool %q needs a name, entries and protect rules", id)
		}
		if id != "agent-run-logs" && tool.Homepage == "" {
			t.Errorf("tool %q needs a homepage", id)
		}
	}
}

// TestAIToolsConfidenceAndAgePolicy pins the policy of docs/catalog.md:
// history is medium and 30 days, caches and logs are high and 14 days, and
// heuristic matches are low.
func TestAIToolsConfidenceAndAgePolicy(t *testing.T) {
	c := embeddedCatalog(t, "linux")
	for _, tool := range c.Tools() {
		if tool.Category != CategoryAI {
			continue
		}
		for i, e := range tool.Entries {
			checkEntryPolicy(t, fmt.Sprintf("%s entries[%d]", tool.ID, i), tool.ID, e)
		}
	}
}

// checkEntryPolicy reports every policy violation of one entry.
func checkEntryPolicy(t *testing.T, label, toolID string, e Entry) {
	t.Helper()
	if !strings.HasPrefix(e.Source, "https://") {
		t.Errorf("%s: source %q must start with https://", label, e.Source)
	}
	if strings.TrimSpace(e.Description) == "" {
		t.Errorf("%s: empty description", label)
	}
	if e.Confidence == ConfidenceLow && toolID != "agent-run-logs" {
		t.Errorf("%s: only the generic tool may be low confidence", label)
	}
	want := map[Confidence]int{ConfidenceMedium: 30, ConfidenceHigh: 14, ConfidenceLow: 30}[e.Confidence]
	if e.MinAgeDays == nil || *e.MinAgeDays != want {
		t.Errorf("%s: %s entries need min_age_days %d, got %v", label, e.Confidence, want, e.MinAgeDays)
	}
}

// TestAIToolsNeverListWorktrees guards the rule that agent created git
// worktrees belong to the worktrees detector: no entry may name them.
func TestAIToolsNeverListWorktrees(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, tool := range embeddedCatalog(t, goos).Tools() {
			for _, e := range tool.Entries {
				for _, p := range e.Patterns {
					if strings.Contains(p, "worktree") {
						t.Errorf("%s: entry pattern %q must not cover worktrees", tool.ID, p)
					}
				}
			}
		}
	}
}

// TestAIToolsNoEntryCoversAnyProtect extends the per tool load-time check
// across tools: a wildcard-free entry of one tool must not be an ancestor of
// protected paths of another either.
func TestAIToolsNoEntryCoversAnyProtect(t *testing.T) {
	tools := embeddedCatalog(t, "linux").Tools()
	for _, tool := range tools {
		for _, e := range tool.Entries {
			for _, other := range tools {
				for _, p := range other.Protect {
					if p.Scope != e.Scope {
						continue
					}
					for _, ep := range e.Patterns {
						for _, pp := range p.Patterns {
							if entryCoversProtected(ep, pp) {
								t.Errorf("%s entry %q covers protected %q of %s", tool.ID, ep, pp, other.ID)
							}
						}
					}
				}
			}
		}
	}
}

func TestAIToolsProjectProtectWins(t *testing.T) {
	m := embeddedCatalog(t, "linux").ProjectMatcher()
	tests := []struct {
		rel   string
		isDir bool
	}{
		{".claude/settings.json", false},
		{".claude/settings.local.json", false},
		{".claude/commands/x.md", false},
		{".claude/agents/a.md", false},
		{".claude/skills/s/SKILL.md", false},
		{".claude/worktrees", true},
		{".claude/worktrees/x/file", false},
		{".claude/worktrees/x/.aider.chat.history.md", false},
		{".worktrees/x/file", false},
		{"packages/app/.worktrees/x/file", false},
		{"CLAUDE.md", false},
		{"CLAUDE.local.md", false},
		{".aider.conf.yml", false},
		{".aiderignore", false},
		{".env", false},
		{".cursorrules", false},
		{".cursor/rules/a.mdc", false},
		{".cursor/mcp.json", false},
		{"AGENTS.md", false},
		{".codex/config.toml", false},
		{"GEMINI.md", false},
		{".gemini/settings.json", false},
		{".continue/config.yaml", false},
		{".clinerules/rules.md", false},
		{".roo/rules/a.md", false},
		{".windsurf/rules/a.md", false},
		{".goosehints", false},
		{".openhands/microagents/repo.md", false},
		{".github/copilot-instructions.md", false},
		{".zed/settings.json", false},
		{".agent/skills/s/runs/x.jsonl", false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			if !m.Protected(tt.rel) {
				t.Errorf("Protected(%q) = false", tt.rel)
			}
			if got, ok := m.Match(tt.rel, tt.isDir); ok {
				t.Errorf("Match(%q) = %+v, want no match", tt.rel, got)
			}
		})
	}
}

func TestAIToolsProjectEntries(t *testing.T) {
	m := embeddedCatalog(t, "linux").ProjectMatcher()
	tests := []struct {
		rel    string
		isDir  bool
		tool   string
		conf   Confidence
		minAge int
		match  bool
	}{
		{".aider.chat.history.md", false, "aider", ConfidenceMedium, 30, true},
		{"sub/dir/.aider.chat.history.md", false, "aider", ConfidenceMedium, 30, true},
		{".aider.input.history", false, "aider", ConfidenceMedium, 30, true},
		{".aider.tags.cache.v3", true, "aider", ConfidenceHigh, 14, true},
		{".aider.tags.cache.v4", true, "aider", ConfidenceHigh, 14, true},
		{".aider.tags.cache.v4", false, "", "", 0, false},
		{".agent/runs/2026/run-1.jsonl", false, "agent-run-logs", ConfidenceLow, 30, true},
		{".agent-runs/run-1.jsonl", false, "agent-run-logs", ConfidenceLow, 30, true},
		{".agent/runs/run-1.txt", false, "", "", 0, false},
		{"src/main.go", false, "", "", 0, false},
		{".claude", true, "", "", 0, false},
		{".aider.chat.history.md", true, "", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			got, ok := m.Match(tt.rel, tt.isDir)
			if ok != tt.match {
				t.Fatalf("Match(%q, dir=%v) = %v, want %v", tt.rel, tt.isDir, ok, tt.match)
			}
			if !ok {
				return
			}
			if got.ToolID != tt.tool || got.Entry.Confidence != tt.conf || got.Entry.MinAgeDays == nil || *got.Entry.MinAgeDays != tt.minAge {
				t.Errorf("Match(%q) = tool %s conf %s age %v", tt.rel, got.ToolID, got.Entry.Confidence, got.Entry.MinAgeDays)
			}
		})
	}
}

// aiMachine is a throwaway home whose per-OS variables point below one temp
// directory, so user locations of every simulated OS can be expanded.
type aiMachine struct {
	home string
	env  func(goos string) PathEnv
}

func newAIMachine(t *testing.T) aiMachine {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	vars := map[string]string{
		"APPDATA":      filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
	}
	return aiMachine{home: home, env: func(goos string) PathEnv {
		return PathEnv{GOOS: goos, Home: home, Getenv: func(k string) string { return vars[k] }}
	}}
}

// userPath joins slash separated elements below the fake home.
func (m aiMachine) userPath(rel string) string {
	return filepath.Join(m.home, filepath.FromSlash(rel))
}

// ensureBase creates the parent directory of abs so that the Base of any
// location that could designate abs exists; Match itself is string based.
func ensureBase(t *testing.T, abs string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
}

// findMatch returns the first location of the catalog that designates abs.
func findMatch(locs []UserLocation, abs string, isDir bool) (UserLocation, bool) {
	for _, l := range locs {
		if l.Match(abs, isDir) {
			return l, true
		}
	}
	return UserLocation{}, false
}

func TestAIToolsUserEntries(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		rel    string
		isDir  bool
		tool   string
		conf   Confidence
		minAge int
	}{
		{"claude transcript", "linux", ".claude/projects/-home-u-app/abc.jsonl", false, "claude-code", ConfidenceMedium, 30},
		{"claude transcript mac", "darwin", ".claude/projects/-Users-u-app/abc.jsonl", false, "claude-code", ConfidenceMedium, 30},
		{"claude transcript win", "windows", ".claude/projects/C--Users-u-app/abc.jsonl", false, "claude-code", ConfidenceMedium, 30},
		{"claude todos", "linux", ".claude/todos/a.json", false, "claude-code", ConfidenceMedium, 30},
		{"claude file history", "linux", ".claude/file-history/session-1", true, "claude-code", ConfidenceMedium, 30},
		{"claude shell snapshot", "linux", ".claude/shell-snapshots/snapshot-zsh-1.sh", false, "claude-code", ConfidenceHigh, 14},
		{"claude debug", "linux", ".claude/debug/abc.txt", false, "claude-code", ConfidenceHigh, 14},
		{"claude statsig", "linux", ".claude/statsig/cache.json", false, "claude-code", ConfidenceHigh, 14},
		{"cursor logs linux", "linux", ".config/Cursor/logs/20260101T000000", true, "cursor", ConfidenceHigh, 14},
		{"cursor logs mac", "darwin", "Library/Application Support/Cursor/logs/20260101T000000", true, "cursor", ConfidenceHigh, 14},
		{"cursor logs win", "windows", "AppData/Roaming/Cursor/logs/20260101T000000", true, "cursor", ConfidenceHigh, 14},
		{"cursor cache", "linux", ".config/Cursor/Cache/Cache_Data", true, "cursor", ConfidenceHigh, 14},
		{"cursor cached data", "darwin", "Library/Application Support/Cursor/CachedData/abc123", true, "cursor", ConfidenceHigh, 14},
		{"cursor workspace storage", "linux", ".config/Cursor/User/workspaceStorage/0123abcd", true, "cursor", ConfidenceMedium, 30},
		{"cursor workspace storage win", "windows", "AppData/Roaming/Cursor/User/workspaceStorage/0123abcd", true, "cursor", ConfidenceMedium, 30},
		{"cursor transcript", "linux", ".cursor/projects/home-u-app/agent-transcripts/2026/abc.jsonl", false, "cursor", ConfidenceMedium, 30},
		{"copilot log", "linux", ".config/Code/logs/20260101T000000/window1/exthost/GitHub.copilot-chat/GitHub Copilot Chat.log", false, "github-copilot", ConfidenceHigh, 14},
		{"codex session", "linux", ".codex/sessions/2026/01/02/rollout-1.jsonl", false, "codex-cli", ConfidenceMedium, 30},
		{"codex log", "linux", ".codex/log/codex-tui.log", false, "codex-cli", ConfidenceHigh, 14},
		{"codex session win", "windows", ".codex/sessions/2026/01/02/rollout-1.jsonl", false, "codex-cli", ConfidenceMedium, 30},
		{"gemini chat", "linux", ".gemini/tmp/proj/chats/session-1.json", false, "gemini-cli", ConfidenceMedium, 30},
		{"gemini checkpoint", "linux", ".gemini/tmp/proj/checkpoints/cp.json", false, "gemini-cli", ConfidenceMedium, 30},
		{"gemini logs", "linux", ".gemini/tmp/proj/logs/logs.json", false, "gemini-cli", ConfidenceHigh, 14},
		{"continue session", "linux", ".continue/sessions/abc.json", false, "continue", ConfidenceMedium, 30},
		{"continue index file", "linux", ".continue/index/index.sqlite", false, "continue", ConfidenceHigh, 14},
		{"continue index dir", "linux", ".continue/index/lancedb", true, "continue", ConfidenceHigh, 14},
		{"continue log", "linux", ".continue/logs/core.log", false, "continue", ConfidenceHigh, 14},
		{"cline tasks", "linux", ".config/Code/User/globalStorage/saoudrizwan.claude-dev/tasks/1700000000000", true, "cline", ConfidenceMedium, 30},
		{"cline tasks mac cursor", "darwin", "Library/Application Support/Cursor/User/globalStorage/saoudrizwan.claude-dev/tasks/1", true, "cline", ConfidenceMedium, 30},
		{"roo tasks win", "windows", "AppData/Roaming/Code/User/globalStorage/rooveterinaryinc.roo-cline/tasks/uuid", true, "roo-code", ConfidenceMedium, 30},
		{"windsurf logs", "linux", ".config/Windsurf/logs/20260101T000000", true, "windsurf", ConfidenceHigh, 14},
		{"windsurf cache mac", "darwin", "Library/Application Support/Windsurf/Cache/Cache_Data", true, "windsurf", ConfidenceHigh, 14},
		{"goose sessions", "linux", ".local/share/goose/sessions/20260101_1.jsonl", false, "goose", ConfidenceMedium, 30},
		{"goose logs", "linux", ".local/state/goose/logs/cli", true, "goose", ConfidenceHigh, 14},
		{"openhands sessions", "linux", ".openhands/sessions/abc", true, "openhands", ConfidenceMedium, 30},
		{"amp threads", "linux", ".local/share/amp/threads/T-1.json", false, "amp", ConfidenceMedium, 30},
		{"zed threads linux", "linux", ".local/share/zed/threads/threads.db", false, "zed", ConfidenceMedium, 30},
		{"zed threads wal", "linux", ".local/share/zed/threads/threads.db-wal", false, "zed", ConfidenceMedium, 30},
		{"zed threads mac", "darwin", "Library/Application Support/Zed/threads/threads.db", false, "zed", ConfidenceMedium, 30},
		{"zed threads win", "windows", "AppData/Local/Zed/threads/threads.db", false, "zed", ConfidenceMedium, 30},
		{"zed old log mac", "darwin", "Library/Logs/Zed/Zed.log.old", false, "zed", ConfidenceHigh, 14},
		{"zed old log win", "windows", "AppData/Local/Zed/logs/Zed.log.old", false, "zed", ConfidenceHigh, 14},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newAIMachine(t)
			abs := m.userPath(tt.rel)
			ensureBase(t, abs)
			locs := embeddedCatalog(t, tt.goos).UserLocations(m.env(tt.goos))
			loc, ok := findMatch(locs, abs, tt.isDir)
			if !ok {
				t.Fatalf("no location designates %s", abs)
			}
			if loc.ToolID != tt.tool || loc.Entry.Confidence != tt.conf || loc.Entry.MinAgeDays == nil || *loc.Entry.MinAgeDays != tt.minAge {
				t.Errorf("got tool %s conf %s age %v, want %s %s %d", loc.ToolID, loc.Entry.Confidence, loc.Entry.MinAgeDays, tt.tool, tt.conf, tt.minAge)
			}
			// Protection must not claim what an entry designates.
			if embeddedCatalog(t, tt.goos).UserProtection(m.env(tt.goos)).Protected(abs) {
				t.Errorf("%s is designated by an entry and protected at once", abs)
			}
		})
	}
}

// TestAIToolsUserProtectWins asserts that configuration and credentials are
// protected and that no entry designates them, on every OS where the path
// exists.
func TestAIToolsUserProtectWins(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		rel   string
		isDir bool
	}{
		{"claude settings", "linux", ".claude/settings.json", false},
		{"claude local settings", "linux", ".claude/settings.local.json", false},
		{"claude command", "linux", ".claude/commands/x.md", false},
		{"claude agent", "linux", ".claude/agents/a.md", false},
		{"claude skill", "linux", ".claude/skills/s/SKILL.md", false},
		{"claude hooks", "linux", ".claude/hooks/pre.sh", false},
		{"claude user memory file", "linux", ".claude/CLAUDE.md", false},
		{"claude auto memory", "linux", ".claude/projects/-home-u-app/memory/MEMORY.md", false},
		{"claude json", "darwin", ".claude.json", false},
		{"claude credentials", "windows", ".claude/.credentials.json", false},
		{"cursor settings", "linux", ".config/Cursor/User/settings.json", false},
		{"cursor keybindings mac", "darwin", "Library/Application Support/Cursor/User/keybindings.json", false},
		{"cursor snippets win", "windows", "AppData/Roaming/Cursor/User/snippets/go.json", false},
		{"cursor global state", "linux", ".config/Cursor/User/globalStorage/state.vscdb", false},
		{"cursor mcp", "linux", ".cursor/mcp.json", false},
		{"cursor rules", "linux", ".cursor/rules/a.mdc", false},
		{"aider user config", "linux", ".aider.conf.yml", false},
		{"copilot hosts", "linux", ".config/github-copilot/hosts.json", false},
		{"copilot apps", "darwin", ".config/github-copilot/apps.json", false},
		{"copilot win hosts", "windows", "AppData/Local/github-copilot/hosts.json", false},
		{"codex config", "linux", ".codex/config.toml", false},
		{"codex auth", "linux", ".codex/auth.json", false},
		{"codex agents", "linux", ".codex/AGENTS.md", false},
		{"codex prompt", "linux", ".codex/prompts/p.md", false},
		{"gemini md", "linux", ".gemini/GEMINI.md", false},
		{"gemini settings", "linux", ".gemini/settings.json", false},
		{"gemini creds", "linux", ".gemini/oauth_creds.json", false},
		{"gemini shadow repo", "linux", ".gemini/history/abc/HEAD", false},
		{"continue config json", "linux", ".continue/config.json", false},
		{"continue config yaml", "linux", ".continue/config.yaml", false},
		{"continue rc", "linux", ".continue/.continuerc.json", false},
		{"continue session index", "linux", ".continue/sessions/sessions.json", false},
		{"cline settings", "linux", ".config/Code/User/globalStorage/saoudrizwan.claude-dev/settings/cline_mcp_settings.json", false},
		{"roo settings", "darwin", "Library/Application Support/Code/User/globalStorage/rooveterinaryinc.roo-cline/settings/custom_modes.yaml", false},
		{"windsurf memories", "linux", ".codeium/windsurf/memories/global_rules.md", false},
		{"windsurf mcp", "linux", ".codeium/windsurf/mcp_config.json", false},
		{"goose config", "linux", ".config/goose/config.yaml", false},
		{"goose secrets win", "windows", "AppData/Roaming/Block/goose/config/secrets.yaml", false},
		{"openhands settings", "linux", ".openhands/settings.json", false},
		{"amp settings", "linux", ".config/amp/settings.json", false},
		{"amp secrets", "linux", ".local/share/amp/secrets.json", false},
		{"zed settings", "linux", ".config/zed/settings.json", false},
		{"zed settings mac", "darwin", ".config/zed/settings.json", false},
		{"zed settings win", "windows", "AppData/Roaming/Zed/settings.json", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newAIMachine(t)
			abs := m.userPath(tt.rel)
			ensureBase(t, abs)
			c := embeddedCatalog(t, tt.goos)
			env := m.env(tt.goos)
			if !c.UserProtection(env).Protected(abs) {
				t.Errorf("%s is not protected", abs)
			}
			if loc, ok := findMatch(c.UserLocations(env), abs, tt.isDir); ok {
				t.Errorf("%s is designated by %s", abs, loc.ToolID)
			}
		})
	}
}

// TestAIToolsOSFilters checks that OS specific entries do not leak into the
// wrong OS, so an unverified layout is never guessed for it.
func TestAIToolsOSFilters(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		rel   string
		isDir bool
	}{
		{"goose sessions are linux only", "darwin", ".local/share/goose/sessions/a.jsonl", false},
		{"amp threads are not on windows", "windows", ".local/share/amp/threads/T-1.json", false},
		{"cursor mac path on linux", "linux", "Library/Application Support/Cursor/logs/x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newAIMachine(t)
			abs := m.userPath(tt.rel)
			ensureBase(t, abs)
			locs := embeddedCatalog(t, tt.goos).UserLocations(m.env(tt.goos))
			if loc, ok := findMatch(locs, abs, tt.isDir); ok {
				t.Errorf("%s designated by %s on %s", abs, loc.ToolID, tt.goos)
			}
		})
	}
}
