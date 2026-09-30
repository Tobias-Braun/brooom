// Package all links every detector into the binary. Each detector package
// registers itself with detect.Register from its init function; adding a
// detector means adding one blank import here.
package all

import (
	_ "github.com/Tobias-Braun/brooom/internal/detectors/aiartifacts"
	_ "github.com/Tobias-Braun/brooom/internal/detectors/buildartifacts"
	_ "github.com/Tobias-Braun/brooom/internal/detectors/gitbloat"
	_ "github.com/Tobias-Braun/brooom/internal/detectors/largeuntracked"
	_ "github.com/Tobias-Braun/brooom/internal/detectors/logs"
	_ "github.com/Tobias-Braun/brooom/internal/detectors/mergedbranch"
	_ "github.com/Tobias-Braun/brooom/internal/detectors/stalebranch"
	_ "github.com/Tobias-Braun/brooom/internal/detectors/worktrees"
)
