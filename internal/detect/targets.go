package detect

import (
	"context"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// TargetSource is an optional interface of a Detector that needs user-level
// targets (for example the directory Claude Code keeps per repository below
// the home directory) in addition to the repositories and project folders the
// scan pipeline builds from the working directory or the path argument. It
// receives the repositories of the scan, so the locations it declares can be
// limited to the data of those repositories.
//
// The pipeline calls ExtraTargets once per scan on every selected detector
// that is enabled in the global configuration, appends the returned targets
// (deduplicated by path) and allows their paths in the scope.Guard. It lives
// here, not in the CLI, so detector packages never import internal/cli.
//
// Implementations must only return user-level locations (Kind TargetUser,
// Scope of type user) and must not perform detection or modify anything:
// they only declare where to look. Locations that do not exist may be
// returned; the pipeline drops them. An error is recorded as a non-fatal
// scan error and does not abort the scan.
type TargetSource interface {
	ExtraTargets(ctx context.Context, cfg *config.Config, repos []string) ([]scope.Target, error)
}
