#!/bin/sh
# Install Tiimi's `tk` from a GitHub release:
#   curl -fsSL https://tiimi.dev/install | sh
#   (or https://raw.githubusercontent.com/tiimihq/tk/main/install.sh)
#
# Options (environment):
#   TK_VERSION      version to install, e.g. 0.1.0 (default: latest release)
#   TK_INSTALL_DIR  where to put tk (default: ~/.local/bin)
#   TK_REPO         GitHub owner/repo (default below)
#   TK_RELEASE_URL  base URL of the release files (default: the GitHub release; for testing)
#
# It downloads the archive for this OS and CPU plus checksums.txt, refuses to
# install unless the SHA-256 matches, and never needs sudo.
set -eu

REPO="${TK_REPO:-tiimihq/tk}"
INSTALL_DIR="${TK_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
fail() { printf 'tk install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "needs $1"; }

need curl
need tar
need uname

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin|linux) ;;
  *) fail "unsupported OS $os (on Windows, download the zip from the releases page)" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported CPU $arch" ;;
esac

version="${TK_VERSION:-}"
if [ -z "$version" ]; then
  # The latest-release page redirects to .../releases/tag/vX.Y.Z
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
    fail "can't reach GitHub to find the latest release"
  version=${url##*/}
fi
version=${version#v}
[ -n "$version" ] || fail "can't tell which version to install"

base="${TK_RELEASE_URL:-https://github.com/$REPO/releases/download/v$version}"
archive="tk_${version}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading tk $version for $os/$arch"
curl -fsSL "$base/$archive" -o "$tmp/$archive" || fail "download failed: $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f || $2 == "*"f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
  fail "needs sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive; not installing"

tar -xzf "$tmp/$archive" -C "$tmp" tk || fail "archive has no tk binary"
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/tk" "$INSTALL_DIR/tk" 2>/dev/null || { cp "$tmp/tk" "$INSTALL_DIR/tk" && chmod 0755 "$INSTALL_DIR/tk"; }
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$INSTALL_DIR/tk" 2>/dev/null || true
fi

say "Installed $("$INSTALL_DIR/tk" version) to $INSTALL_DIR/tk (checksum verified)"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "Add it to your PATH:  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
say "Next: tk new \"First issue\" && tk board    (Claude Code hook: tk hook install, in your project)"
