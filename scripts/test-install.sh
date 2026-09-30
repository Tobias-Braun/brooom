#!/bin/sh
# Smoke test for scripts/install.sh. It serves a fake release from a local
# python3 http.server (through BROOOM_DOWNLOAD_BASE) and checks a successful
# install, the br shortcut, a tampered checksum, an unknown version and the
# PATH warning.
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
# The server script lives in a file rather than a heredoc on the interpreter's
# stdin, and its stderr goes to a log that is only shown when startup fails.
server_log="$work/server.log"
cat > "$work/server.py" <<'PY'
import functools, http.server, socketserver, sys

# HTTPServer.server_bind resolves the bound address with socket.getfqdn, a
# reverse DNS lookup that hangs for many seconds on macOS runners. Only the
# port is needed here, so bind through TCPServer directly.
class Server(http.server.ThreadingHTTPServer):
    def server_bind(self):
        socketserver.TCPServer.server_bind(self)
        self.server_port = self.socket.getsockname()[1]

handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=sys.argv[1])
handler.log_message = lambda *a, **k: None
srv = Server(("127.0.0.1", 0), handler)
with open(sys.argv[2], "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
PY
python3 -u "$work/server.py" "$site" "$port_file" >"$server_log" 2>&1 </dev/null &
server_pid=$!

i=0
while [ ! -s "$port_file" ]; do
  i=$((i + 1))
  [ "$i" -lt 100 ] || fail "http server did not start ($(command -v python3), pid $server_pid): $(cat "$server_log" 2>/dev/null)"
  sleep 0.1
done
port=$(cat "$port_file")
BROOOM_DOWNLOAD_BASE="http://127.0.0.1:$port"
export BROOOM_DOWNLOAD_BASE

# An empty HOME keeps the developer's rc files and a real broot install from
# deciding whether the br shortcut is free.
HOME="$work/home"
mkdir -p "$HOME"
export HOME

# 1. Successful install into a directory that is not on PATH.
dest="$work/bin"
out=$(BROOOM_VERSION="$version" BROOOM_INSTALL_DIR="$dest" sh "$installer" 2>&1) || fail "install failed: $out"
[ -x "$dest/brooom" ] || fail "binary not installed"
[ "$("$dest/brooom")" = "brooom $version" ] || fail "installed binary does not run"
case "$out" in *"not in your PATH"*) ;; *) fail "missing PATH warning: $out" ;; esac

# The v prefix on BROOOM_VERSION is optional.
rm -f "$dest/brooom"
BROOOM_VERSION="$tag" BROOOM_INSTALL_DIR="$dest" sh "$installer" >/dev/null 2>&1 || fail "install with v prefix failed"

# 1b. The br shortcut: installed when free, refreshed on upgrade, skipped with a
# reason (and left untouched) whenever something else already owns br.
if command -v br >/dev/null 2>&1 || command -v broot >/dev/null 2>&1; then
  echo "skipping br shortcut checks: br or broot is on this machine's PATH"
else
  [ -L "$dest/br" ] || fail "br shortcut not installed"
  [ "$("$dest/br")" = "brooom $version" ] || fail "br shortcut does not run brooom"
  out=$(BROOOM_VERSION="$version" BROOOM_INSTALL_DIR="$dest" sh "$installer" 2>&1) || fail "upgrade failed: $out"
  case "$out" in *"Installed $dest/br"*) ;; *) fail "br not refreshed on upgrade: $out" ;; esac

  foreign="$work/foreign"
  mkdir -p "$foreign"
  printf 'mine\n' > "$foreign/br"
  out=$(BROOOM_VERSION="$version" BROOOM_INSTALL_DIR="$foreign" sh "$installer" 2>&1) || fail "install next to a foreign br failed: $out"
  [ "$(cat "$foreign/br")" = "mine" ] || fail "a foreign br was overwritten"
  [ -x "$foreign/brooom" ] || fail "brooom not installed next to a foreign br"
  case "$out" in *"Skipped the br shortcut: $foreign/br already exists"*) ;; *) fail "missing skip reason for a foreign br: $out" ;; esac

  other="$work/other-bin"
  mkdir -p "$other"
  printf '#!/bin/sh\n' > "$other/br"
  chmod 755 "$other/br"
  out=$(PATH="$other:$PATH" BROOOM_VERSION="$version" BROOOM_INSTALL_DIR="$work/onpath" sh "$installer" 2>&1) || fail "install with br on PATH failed: $out"
  [ ! -e "$work/onpath/br" ] || fail "br installed although another br is on PATH"
  case "$out" in *"$other/br is already on your PATH"*) ;; *) fail "missing skip reason for br on PATH: $out" ;; esac

  mkdir -p "$HOME/.config/broot/launcher"
  out=$(BROOOM_VERSION="$version" BROOOM_INSTALL_DIR="$work/broot" sh "$installer" 2>&1) || fail "install with broot failed: $out"
  [ ! -e "$work/broot/br" ] || fail "br installed although broot is set up"
  case "$out" in *"used by broot"*) ;; *) fail "missing broot skip reason: $out" ;; esac
  rm -rf "$HOME/.config"

  printf 'alias br="echo hi"\n' > "$HOME/.zshrc"
  out=$(BROOOM_VERSION="$version" BROOOM_INSTALL_DIR="$work/alias" sh "$installer" 2>&1) || fail "install with a br alias failed: $out"
  [ ! -e "$work/alias/br" ] || fail "br installed although an alias exists"
  case "$out" in *"alias or function in $HOME/.zshrc"*) ;; *) fail "missing alias skip reason: $out" ;; esac
  rm -f "$HOME/.zshrc"
fi

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
