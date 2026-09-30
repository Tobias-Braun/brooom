#!/bin/sh
# Installs the latest (or a chosen) Brooom release from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.sh | sh
#
# Environment:
#   BROOOM_VERSION       version to install, with or without leading "v" (default: latest)
#   BROOOM_INSTALL_DIR   target directory (default: $HOME/.local/bin)
#   BROOOM_DOWNLOAD_BASE base URL holding <tag>/<asset> (default: GitHub releases download URL)
#   BROOOM_LATEST_URL    URL that redirects to the latest tag (default: GitHub releases/latest)
#
# The archive name must match archives.name_template in .goreleaser.yaml:
# brooom_<version>_<os>_<arch>.tar.gz. The script never uses sudo, verifies the
# sha256 checksum before extracting anything and aborts on any mismatch.
set -eu

REPO_URL="https://github.com/Tobias-Braun/brooom/releases"
DOWNLOAD_BASE="${BROOOM_DOWNLOAD_BASE:-$REPO_URL/download}"
LATEST_URL="${BROOOM_LATEST_URL:-$REPO_URL/latest}"

say() { printf '%s\n' "$*"; }
die() { printf 'brooom install: %s\n' "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

detect_os() {
  case "$(uname -s)" in
    Linux) echo linux ;;
    Darwin) echo darwin ;;
    *) die "unsupported operating system '$(uname -s)'; on Windows use scripts/install.ps1" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) die "unsupported architecture '$(uname -m)'; supported: amd64, arm64" ;;
  esac
}

# fetch URL DEST downloads with curl, falling back to wget.
fetch() {
  if have curl; then
    curl -fsSL -o "$2" "$1" || die "download failed: $1"
  elif have wget; then
    wget -q -O "$2" "$1" || die "download failed: $1"
  else
    die "neither curl nor wget found; install one of them"
  fi
}

# latest_tag prints the tag the releases/latest URL redirects to.
latest_tag() {
  url=""
  if have curl; then
    url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$LATEST_URL") || die "cannot resolve latest release from $LATEST_URL"
  elif have wget; then
    url=$(wget -S --spider --max-redirect=0 "$LATEST_URL" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tr -d '\r' | tail -n 1) || true
  else
    die "neither curl nor wget found; install one of them"
  fi
  tag=${url##*/}
  if [ -z "$tag" ] || [ "$tag" = "latest" ]; then
    die "cannot resolve latest release from $LATEST_URL; set BROOOM_VERSION"
  fi
  echo "$tag"
}

sha256_of() {
  if have sha256sum; then
    sha256sum "$1" | cut -d ' ' -f 1
  elif have shasum; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    die "neither sha256sum nor shasum found; cannot verify the download"
  fi
}

main() {
  os=$(detect_os)
  arch=$(detect_arch)

  if [ -n "${BROOOM_VERSION:-}" ]; then
    tag="v${BROOOM_VERSION#v}"
  else
    tag=$(latest_tag)
  fi
  version=${tag#v}
  archive="brooom_${version}_${os}_${arch}.tar.gz"

  install_dir="${BROOOM_INSTALL_DIR:-${HOME:?HOME is not set}/.local/bin}"

  tmp=$(mktemp -d "${TMPDIR:-/tmp}/brooom-install.XXXXXX") || die "cannot create a temporary directory"
  trap 'rm -rf "$tmp"' EXIT INT TERM HUP

  say "Installing brooom $version for $os/$arch"
  fetch "$DOWNLOAD_BASE/$tag/$archive" "$tmp/$archive"
  fetch "$DOWNLOAD_BASE/$tag/checksums.txt" "$tmp/checksums.txt"

  # The checksum file lists "<sha256>  <name>"; match the name exactly.
  expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
  [ -n "$expected" ] || die "no checksum for $archive in checksums.txt"
  actual=$(sha256_of "$tmp/$archive")
  [ "$expected" = "$actual" ] || die "checksum mismatch for $archive (expected $expected, got $actual); aborting"

  tar -xzf "$tmp/$archive" -C "$tmp" brooom || die "cannot extract $archive"
  mkdir -p "$install_dir" || die "cannot create $install_dir"
  # Copy then rename so a running brooom binary is replaced atomically.
  cp "$tmp/brooom" "$install_dir/.brooom.new" || die "cannot write to $install_dir"
  chmod 755 "$install_dir/.brooom.new"
  mv -f "$install_dir/.brooom.new" "$install_dir/brooom"

  say "Installed $install_dir/brooom"
  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *) say "Warning: $install_dir is not in your PATH. Add it, e.g.: export PATH=\"$install_dir:\$PATH\"" ;;
  esac
}

main "$@"
