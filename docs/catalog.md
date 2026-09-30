# The catalog

The catalog is the data that tells Brooom where AI tools and dev tooling leave
clutter (run logs, transcripts, caches, crash dumps, OS junk). It is embedded
JSON in `internal/catalog/data/`, extensible through the config, and read by
the `ai-artifacts` and `log-and-runtime-files` detectors (and later
`build-artifacts`). The configuration files of a tool (settings, instructions,
skills, commands) are never clutter: every tool lists them under `protect`.

The `internal/catalog` package is pure. It never modifies anything and touches
the filesystem only to check which user locations exist.

## File format

Tool files (`ai_tools.json`, `dev_tools.json`) look like this:

```json
{
  "schema_version": 1,
  "tools": [
    {
      "id": "claude-code",
      "name": "Claude Code",
      "category": "ai",
      "homepage": "https://www.anthropic.com/claude-code",
      "entries": [
        {
          "scope": "user",
          "patterns": ["~/.claude/todos"],
          "os": [],
          "kind": "file",
          "confidence": "medium",
          "min_age_days": 30,
          "description": "Per-session todo lists of finished sessions",
          "source": "https://docs.claude.com/en/docs/claude-code/settings"
        }
      ],
      "protect": [
        {
          "scope": "user",
          "patterns": ["~/.claude/settings.json", "~/.claude/skills"],
          "os": [],
          "reason": "Settings and skills are configuration, not clutter"
        }
      ]
    }
  ]
}
```

JSON is decoded strictly: unknown fields are an error, so a typo in a
contributed file fails CI. Only the files named in `toolFiles`
(`internal/catalog/load.go`) use this format; other files under `data/` (for
example `build_artifacts.json`) have their own schema and decoder and are
ignored by the tools loader.

| Field | Values |
| --- | --- |
| `id` | Unique kebab-case id across all files; used in config toggles. |
| `category` | `ai`, `logs`, `cache`, `os-junk`, `crash`, `build`. Toggled through `detectors.log-and-runtime-files.categories`. `crash` is separate from `logs` because dumps are diagnostics some users want to keep. |
| `homepage` | Optional http(s) URL. |
| `entries[].scope` | `project` (patterns relative to a project root) or `user`. |
| `entries[].patterns` | One or more globs, forward slashes only. |
| `entries[].os` | Optional filter of `linux`, `darwin`, `windows`; empty means all. |
| `entries[].kind` | `file` or `dir`. A `file` entry never matches a directory and vice versa (a directory named `core` is not a crash dump). `any` exists only for config extras. |
| `entries[].confidence` | `high`, `medium` or `low`. |
| `entries[].min_age_days` | Optional. An explicit `0` means "no age requirement", unset means "use the detector default". |
| `entries[].description` | Required, one short sentence. |
| `entries[].source` | Optional in the format, required for contributions (see the checklist). |
| `protect[]` | `scope`, `patterns`, `os` as for entries, plus a required `reason`. |

## Pattern syntax

- `*` and `?` never cross `/`; `[abc]`, `[a-z]` and `[!x]` are character
  classes.
- `**` as a whole path segment matches zero or more segments (`a/**/b`
  matches `a/b` and `a/x/y/b`; a trailing `a/**` matches `a` and everything
  below it). `**` inside a segment (`a**b`) is invalid.
- There is no escape character; a trailing or doubled `/` and unbalanced `[`
  are invalid.
- Matching is case-insensitive on Windows and macOS, case-sensitive on Linux.

### Project patterns

Project patterns are gitignore-like:

- A pattern without `/` matches the base name at any depth (`npm-debug.log*`).
- A pattern containing `/` is anchored at the project root
  (`.claude/*.log`).
- A leading `**/` means any depth explicitly.

They must be relative, without `..`, drive letters or backslashes.

### User patterns

User patterns start with one of these variables (anything else is rejected):

| Variable | Linux | macOS | Windows |
| --- | --- | --- | --- |
| `~` | home | home | home (`USERPROFILE`) |
| `$XDG_CACHE_HOME` | value if absolute, else `~/.cache` | same | undefined, skipped |
| `$XDG_DATA_HOME` | value if absolute, else `~/.local/share` | same | undefined, skipped |
| `$XDG_CONFIG_HOME` | value if absolute, else `~/.config` | same | undefined, skipped |
| `~/Library/...` | skipped | home + `Library/...` | skipped |
| `%LOCALAPPDATA%` | undefined, skipped | undefined, skipped | `LOCALAPPDATA` |
| `%APPDATA%` | undefined, skipped | undefined, skipped | `APPDATA` |

A pattern whose variable is undefined on that OS is skipped silently. Write
patterns with forward slashes on every OS; expansion produces native paths. The
first directory below the variable must be literal (`~/.tool/*.log` is fine,
`~/*/x` is not) and `..` is rejected. `**` in a user pattern reaches at most
8 segments below `Base`.

## The `Base` contract

`Catalog.UserLocations(env, cats...)` expands user patterns for one OS and
returns only locations that exist and are readable. `PathEnv{GOOS, Home,
Getenv}` is injectable so tests can simulate any OS.

Each `UserLocation` has a `Base`: the literal directory prefix of the expanded
pattern up to the first wildcard segment (for wildcard-free patterns, the path
itself). `Base` is what gets added to the scope guard. **Detectors emit
findings only strictly below `Base`, never `Base` itself**, because an allowed
location itself can never be removed (`IsAllowedRoot`). For a wildcard-free
pattern such as `~/.claude/todos` they therefore report the entries directly
inside it, not `~/.claude/todos`. `UserLocation.Match(abs, isDir)` implements
exactly this rule, including the kind check and the depth bound.

`Catalog.UserProtection(env)` returns the expanded user-scope protect rules;
detectors must reject every candidate for which `Protected(abs)` is true.

## Protect rules

Protect always wins over entries, whatever their order, and applies whatever
categories a detector scans. `ProjectMatcher.Match` never matches a protected
path or anything below one; `Protected(rel)` is true when the path matches a
protect pattern or lies below a protected path.

Validation adds a self-consistency check: no entry may have a wildcard-free
pattern that equals or is an ancestor directory of a `protect` pattern of the
same tool and scope. An entry `~/.claude` next to the protect rule
`~/.claude/settings.json` fails to load. Use narrower entries such as
`~/.claude/todos`.

## Confidence and minimum age

| Kind of data | Confidence | `min_age_days` |
| --- | --- | --- |
| Session transcripts and history | `medium` | 30 |
| Caches and logs | `high` | 14 |
| OS junk recreated on demand | `high` | 0 |

Use `low` when a match might be something the user still wants.

## Extending through the config

`detectors.ai-artifacts.extra` and `detectors.log-and-runtime-files.extra`
accept the shorthand and the full format (see also [config.md](config.md)).

```json
{
  "detectors": {
    "log-and-runtime-files": {
      "extra": [
        { "id": "my-tool", "name": "My tool", "project": [".mytool/cache"], "user": ["~/.mytool/logs"] },
        {
          "id": "my-other-tool",
          "name": "My other tool",
          "category": "crash",
          "entries": [
            {
              "scope": "project",
              "patterns": ["**/*.dmp"],
              "kind": "file",
              "confidence": "high",
              "min_age_days": 7,
              "description": "Crash dumps of my other tool"
            }
          ],
          "protect": [
            { "scope": "project", "patterns": ["keep/*.dmp"], "reason": "Reference dumps" }
          ]
        }
      ]
    }
  }
}
```

- The shorthand `project`/`user` lists become entries of kind `any`
  (file or directory), confidence `medium`, described by `description` or
  the tool name. Entries in the full format default to kind `any` and
  confidence `medium` when those are omitted.
- An extra with a new id becomes a new tool. Its category defaults to `ai` for
  `ai-artifacts` and `logs` for `log-and-runtime-files`.
- An extra with the id of an embedded tool appends its entries and protect
  rules. The embedded name, category and homepage win, and extras can never
  remove or weaken protect rules.
- Extras are validated when the catalog loads, with every problem reported at
  once naming the extra, tool id and entry index.
- Toggle tools with `tools` (`{"cursor": false}`) and categories with
  `categories` (`{"os-junk": false}`); unlisted ones stay enabled. Entries have
  no `enabled` flag.

## How to contribute an entry

1. Find the tool's real behaviour and note a documentation URL (or issue or
   source link) that proves where it writes the files. The `source` URL is
   required for every entry.
2. Add the tool to `internal/catalog/data/ai_tools.json` (AI tools) or
   `dev_tools.json` (everything else), or add entries to the existing tool.
   Choose the narrowest `patterns`, an exact `kind` (never `any`), a
   `confidence` and `min_age_days` following the table above.
3. Protect the tool's configuration files (settings, instructions, skills,
   commands, hooks) with `protect` rules for both scopes where they exist.
4. Prefer wildcard-free directories or narrow globs; keep `**` for cases that
   need it.
5. Add a test case for a new pattern shape or OS filter to
   `internal/catalog` (existing tests already validate all embedded data:
   `go test ./internal/catalog`).
6. Run `go test ./...`; a typo in the JSON or a violated rule fails loading with
   a message naming the file, tool id and entry.
