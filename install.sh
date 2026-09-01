#!/bin/sh
# Installs the incoda binary on macOS or Linux from a GitHub release.
#
# Downloads the prebuilt binary for this platform, verifies it against the
# release's SHA256SUMS, and installs it. Nothing unverifiable is installed.
#
# usage: [VERSION=v0.1.1] sh install.sh
#        sh install.sh v0.1.1
#
# environment:
#   INCODA_INSTALL_DIR   where to put the binary (default: ~/.local/bin)
#   INCODA_REPO          repository to pull from (default: deblasis/incoda)

set -eu

REPO="${INCODA_REPO:-deblasis/incoda}"
INSTALL_DIR="${INCODA_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${1:-${INCODA_VERSION:-}}"

fail() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# --- preconditions -------------------------------------------------------

command -v curl >/dev/null 2>&1 || fail 'curl is required to download the release.'

# --- pick the asset ------------------------------------------------------

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

ASSET="incoda_${os}_${arch}"

if [ -n "$VERSION" ]; then
  BASE="https://github.com/$REPO/releases/download/$VERSION"
else
  BASE="https://github.com/$REPO/releases/latest/download"
fi

printf 'incoda: installing %s (%s) from %s\n' "${VERSION:-latest}" "$ASSET" "$REPO"

WORK="$(mktemp -d)"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

fetch() { curl -fsSL "$BASE/$1" -o "$WORK/$1" || fail "could not download $1 from $BASE (does the release exist?)"; }

fetch "$ASSET"
fetch SHA256SUMS

# --- verify --------------------------------------------------------------

expected="$(awk -v a="$ASSET" '{ n=$2; sub(/^\*/,"",n); if (n==a) { print $1; exit } }' "$WORK/SHA256SUMS")"
[ -n "$expected" ] || fail "SHA256SUMS has no entry for $ASSET; refusing to install an unverified binary"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$WORK/$ASSET" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$WORK/$ASSET" | awk '{print $1}')"
else
  fail 'neither sha256sum nor shasum is available; cannot verify the download'
fi

if [ "$actual" != "$expected" ]; then
  fail "SHA-256 mismatch for $ASSET: expected $expected, got $actual. NOT installing."
fi
printf 'incoda: sha256 verified (%s)\n' "$actual"

# --- install -------------------------------------------------------------

mkdir -p "$INSTALL_DIR"
TARGET="$INSTALL_DIR/incoda"
cp "$WORK/$ASSET" "$TARGET"
chmod +x "$TARGET"
printf 'incoda: installed to %s\n' "$TARGET"

"$TARGET" version

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) printf '\nincoda: %s is not on your PATH. Add it to your shell profile:\n  export PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR" ;;
esac
