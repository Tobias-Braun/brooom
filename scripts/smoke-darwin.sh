#!/bin/sh
# Smoke test for a shipped macOS binary (CGO_ENABLED=0, purego). Usage:
#   scripts/smoke-darwin.sh path/to/brooom
# It runs `brooom version`, then sweeps an old .DS_Store out of a temp
# workspace into the real macOS Trash, which is the NSFileManager path that
# only the purego build exercises. Everything happens under a temp dir with
# BROOOM_HOME redirected, so nothing of the runner account is touched except
# the Trash entry of the temp file.
set -eu

bin=${1:?usage: smoke-darwin.sh path/to/brooom}
case "$bin" in /*) ;; *) bin="$PWD/$bin" ;; esac

work=$(mktemp -d "${TMPDIR:-/tmp}/brooom-smoke.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM HUP
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

BROOOM_HOME="$work/home"
export BROOOM_HOME
mkdir -p "$work/ws/proj"
# package.json makes the directory a project; the 2020 mtime clears the
# default minimum age of the junk detector.
echo '{}' > "$work/ws/proj/package.json"
junk="$work/ws/proj/.DS_Store"

"$bin" version --format plain || fail "brooom version failed"

plant() { : > "$junk"; touch -t 202001010000 "$junk"; }

echo "== dry run leaves the file"
plant
"$bin" sweep tidy "$work/ws" --dry-run >/dev/null || fail "dry run failed"
[ -f "$junk" ] || fail "dry run removed the file"

echo "== sweep moves the file to the OS trash"
# The Trash may hold its own entries, so compare the entry count.
trash_count() { ls -A "$HOME/.Trash" 2>/dev/null | wc -l | tr -d ' '; }
before=$(trash_count)
"$bin" sweep tidy "$work/ws" --yes >/dev/null || fail "sweep failed"
[ ! -e "$junk" ] || fail "file still present after sweep"
if [ "$(uname -s)" = Darwin ]; then
  after=$(trash_count)
  [ "$after" -gt "$before" ] || fail "no new entry in ~/.Trash (had $before, now $after)"
else
  echo "skipped: the ~/.Trash entry is only checked on macOS"
fi
[ -n "$("$bin" sessions --format plain)" ] || fail "no session recorded"

echo "smoke test passed"
