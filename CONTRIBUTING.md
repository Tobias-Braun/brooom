# Contributing to Brooom

Thanks for helping keep workspaces clean!

## Development

Requirements: Go (version from `go.mod`), git, and optionally
[golangci-lint](https://golangci-lint.run/) v2.

```sh
go build ./cmd/brooom          # build
go test ./...                  # unit and integration tests
golangci-lint run ./...        # lint
```

Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) first: it describes the
package layout, the detector/action/trash/output contracts and the
conventions every change follows.

## Pull requests

- One issue per PR; reference it with `Closes #<n>`.
- Branch names: `<type>/<issue>-<short-title>`, e.g. `feat/12-merged-branch`.
- `go vet ./...`, `golangci-lint run ./...` and `go test ./...` must pass.
  CI runs the tests on Linux, macOS and Windows and cross-builds all release
  targets.
- Anything that can modify or delete data needs tests for the refusal paths
  (outside scope, symlinks, dirty state, open files), not only the happy path.

## Adding a detector

1. Create `internal/detectors/<name>/` implementing `detect.Detector`.
2. Register it in `init()` and add a blank import to
   `internal/detectors/all/all.go`.
3. Add its config block to `internal/config` with safe defaults.
4. Test it against real repos/trees built with `internal/testutil`.

## Adding a tool to the AI artifacts catalog

Tool locations are data, not code: see `docs/catalog.md` (added with the
ai-artifacts detector) for the entry format.
