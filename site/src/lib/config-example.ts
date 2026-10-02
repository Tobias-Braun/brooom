/**
 * Every key below exists in internal/config/config.go and the values are
 * valid: sweep.preset, thresholds.min_age_days, detectors.<name>.enabled and
 * output.format. If Default() changes,
 * the code wins and this excerpt must follow.
 *
 * A single constant feeds both the visible code block and the copy button,
 * so what is copied can never drift from what is shown.
 */
export const configJson = `{
  "version": 1,
  "sweep": { "preset": "after-agents" },
  "thresholds": { "min_age_days": 30 },
  "detectors": {
    "git-bloat": { "enabled": false }
  },
  "output": { "format": "summary" }
}`;
