package cli

import (
	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
)

func newLogsCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Find (and trash) logs, caches and runtime leftovers of dev tools",
		Long: `Report log and runtime files of general dev tooling: npm/yarn/pnpm debug
logs, pip/poetry/uv caches, Jest/Vitest/pytest caches, coverage output,
.DS_Store, Thumbs.db, editor swap files, crash dumps and rotated logs. Files
that are currently open by a process are flagged, never suggested.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCleanup(cmd, cleanupSelection{detectors: []string{config.DetectorLogs}, label: "logs"}, af)
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}

func newArtifactsCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "artifacts",
		Short: "Find (and trash) build artifacts of inactive projects",
		Long: `Report build output and dependency folders (node_modules, dist, build,
target, .venv, __pycache__, .next, .turbo, .gradle, ...) weighted by how long
the project has been inactive.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCleanup(cmd, cleanupSelection{detectors: []string{config.DetectorBuildArtifacts}, label: "artifacts"}, af)
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}

func newAICmd(a *app) *cobra.Command {
	var af applyFlags
	var user bool
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "Find (and trash) AI agent artifacts: run logs, transcripts, caches",
		Long: `Report artifacts left by AI coding tools (Claude Code, Cursor, Aider,
Copilot, Codex, ...): run logs, JSONL transcripts, caches and scratch files in
projects, and with --user also in well-known user-level locations. The list
of tools is maintained as data; see docs/catalog.md to contribute entries.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCleanup(cmd, cleanupSelection{detectors: []string{config.DetectorAIArtifacts}, userLocations: user, label: "ai"}, af)
		},
	}
	cmd.Flags().BoolVar(&user, "user", false, "also scan user-level tool locations (caches, logs)")
	addApplyFlags(cmd, &af)
	return cmd
}
