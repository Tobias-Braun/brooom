#!/bin/sh
# Smoke test for scripts/install.sh. It serves a fake release from a local
# python3 http.server (through BROOOM_DOWNLOAD_BASE) and checks a successful
# install, a tampered checksum, an unknown version and the PATH warning.
# Runs on Linux and macOS; CI runs it on both.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
installer="$here/install.sh"

work=$(mktemp -d "${TMPDIR:-/tmp}/brooom-install-test.XXXXXX")
server_pid=""
cleanup() {
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT INT TERM HUP

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "skipping: unsupported OS"; exit 0 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "skipping: unsupported arch"; exit 0 ;;
esac

version=1.2.3
tag="v$version"
archive="brooom_${version}_${os}_${arch}.tar.gz"
site="$work/site"
mkdir -p "$site/$tag" "$work/pkg"

# The fake binary only has to be executable and identify itself.
printf '#!/bin/sh\necho "brooom %s"\n' "$version" > "$work/pkg/brooom"
chmod 755 "$work/pkg/brooom"
tar -czf "$site/$tag/$archive" -C "$work/pkg" brooom

if command -v sha256sum >/dev/null 2>&1; then
  sum=$(sha256sum "$site/$tag/$archive" | cut -d ' ' -f 1)
else
  sum=$(shasum -a 256 "$site/$tag/$archive" | cut -d ' ' -f 1)
fi
printf '%s  %s\n' "$sum" "$archive" > "$site/$tag/checksums.txt"

port_file="$work/port"
# stderr is dropped because the request log only adds noise to the output.
python3 - "$site" "$port_file" 2>/dev/null <<'PY' &
import functools, http.server, sys
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=sys.argv[1])
handler.log_message = lambda *a, **k: None
srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
with open(sys.argv[2], "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
PY
server_pid=$!

i=0
while [ ! -s "$port_file" ]; do
  i=$((i + 1))
  [ "$i" -lt 100 ] || fail "http server did not start"
  sleep 0.1
done
port=$(cat "$port_file")
BROOOM_DOWNLOAD_BASE="http://127.0.0.1:$port"
export BROOOM_DOWNLOAD_BASE

# 1. Successful install into a directory that is not on PATH.
dest="$work/bin"
out=$(BROOOM_VERSION="$version" BROOOM_INSTALL_DIR="$dest" sh "$installer" 2>&1) || fail "install failed: $out"
[ -x "$dest/brooom" ] || fail "binary not installed"
[ "$("$dest/brooom")" = "brooom $version" ] || fail "installed binary does not run"
case "$out" in *"not in your PATH"*) ;; *) fail "missing PATH warning: $out" ;; esac

# The v prefix on BROOOM_VERSION is optional.
rm -f "$dest/brooom"
BROOOM_VERSION="$tag" BROOOM_INSTALL_DIR="$dest" sh "$installer" >/dev/null 2>&1 || fail "install with v prefix failed"

# 2. Tampered archive: checksum verification must abort and install nothing.
tamper="$work/tamper"
printf 'tampered' >> "$site/$tag/$archive"
if out=$(BROOOM_VERSION="$version" BROOOM_INSTALL_DIR="$tamper" sh "$installer" 2>&1); then
  fail "tampered download was accepted"
fi
case "$out" in *"checksum mismatch"*) ;; *) fail "expected checksum mismatch error, got: $out" ;; esac
[ ! -e "$tamper/brooom" ] || fail "tampered binary was installed"

# 3. Unknown version fails with a download error.
if BROOOM_VERSION=9.9.9 BROOOM_INSTALL_DIR="$work/none" sh "$installer" >/dev/null 2>&1; then
  fail "unknown version was accepted"
fi

echo "install.sh: all checks passed"
