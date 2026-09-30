package buildartifacts

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// activity is what tells whether a project is still being worked on: the
// last commit that touched it and its newest source file. Generated
// directories deliberately do not count, because building or installing
// updates them without anyone working on the project.
type activity struct {
	commit time.Time
	source time.Time
}

// last is the newer of the two signals; zero means neither is known.
func (a activity) last() time.Time {
	if a.commit.After(a.source) {
		return a.commit
	}
	return a.source
}

// activityCache computes the activity of each project directory once, so
// several artifact directories of one project cost a single walk and a
// single git call.
type activityCache struct {
	mu      sync.Mutex
	entries map[string]*activityEntry
}

type activityEntry struct {
	once sync.Once
	val  activity
}

func newActivityCache() *activityCache {
	return &activityCache{entries: map[string]*activityEntry{}}
}

// projectActivity returns the activity of the project directory parentRel
// (relative to the scan root, "" for the root).
func (s *scan) projectActivity(ctx context.Context, parentRel string) activity {
	s.acts.mu.Lock()
	e, ok := s.acts.entries[parentRel]
	if !ok {
		e = &activityEntry{}
		s.acts.entries[parentRel] = e
	}
	s.acts.mu.Unlock()
	e.once.Do(func() {
		e.val = activity{commit: s.lastCommit(ctx, parentRel), source: s.newestSource(ctx, parentRel)}
	})
	return e.val
}

// lastCommit is the author-independent commit time of the newest commit
// touching the project directory. Non-git targets, fresh repositories
// without commits and git failures all mean "no commit signal", so only file
// times decide.
func (s *scan) lastCommit(ctx context.Context, parentRel string) time.Time {
	if s.target.Kind != scope.TargetRepo {
		return time.Time{}
	}
	spec := "."
	if parentRel != "" {
		spec = literalPathspec(parentRel)
	}
	out, err := s.env.Git.Run(ctx, s.root, "log", "-1", "--format=%ct", "--", spec)
	if err != nil {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}

// newestSource is the newest mtime of any regular file in the project
// directory, skipping everything the candidate walk skips (artifact
// directories, excluded directories, nested repositories, skip dirs, .git).
func (s *scan) newestSource(ctx context.Context, parentRel string) time.Time {
	dir := s.root
	if parentRel != "" {
		dir = joinPath(s.root, parentRel)
	}
	var mu sync.Mutex
	var newest time.Time
	_ = walk.Walk(ctx, dir, s.walkOptions(), func(e walk.Entry) walk.Decision {
		if !e.IsDir() {
			if e.Type.IsRegular() {
				mu.Lock()
				if e.ModTime.After(newest) {
					newest = e.ModTime
				}
				mu.Unlock()
			}
			return walk.Continue
		}
		return s.sourceDir(e, joinRel(parentRel, e.Rel))
	}, nil)
	return newest
}

// sourceDir decides whether the activity walk enters a directory.
func (s *scan) sourceDir(e walk.Entry, rel string) walk.Decision {
	if s.pruned(e.Name, rel) || isNestedRepo(e.Path) {
		return walk.SkipDir
	}
	if _, ok := s.m.match(rel, true); ok {
		return walk.SkipDir
	}
	return walk.Continue
}

func joinRel(parent, rel string) string {
	if parent == "" {
		return rel
	}
	return parent + "/" + rel
}
