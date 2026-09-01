#!/bin/sh
# Installs the incoda binary on macOS or Linux from a private GitHub release.
#
# The repository is private, so release assets cannot be fetched with a plain
# curl: they need an authenticated API call. This script uses the gh CLI for
# that, verifies the download against SHA256SUMS, and refuses to install
# anything it could not verify.
#
# usage: ./install.sh [VERSION]     (VERSION defaults to the latest release)
#
# environment:
#   INCODA_INSTALL_DIR   where to put the binary (default: ~/.local/bin)
#   INCODA_REPO          repository to pull from (default: deblasis/incoda)

set -eu

REPO="${INCODA_REPO:-deblasis/incoda}"
INSTALL_DIR="${INCODA_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${1:-}"

fail() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# --- preconditions -------------------------------------------------------

command -v gh >/dev/null 2>&1 || fail 'gh (the GitHub CLI) is not on PATH.

incoda is distributed from a PRIVATE repository, so its release assets cannot be
downloaded without authentication. Install gh from https://cli.github.com/ and
run `gh auth login`, then re-run this script.'

gh auth status >/dev/null 2>&1 || fail 'gh is installed but not authenticated. Run `gh auth login` and try again.

Downloading assets from a PRIVATE repository needs a token with `repo` scope.
Check yours with `gh auth status`.'

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

if [ -z "$VERSION" ]; then
  VERSION="$(gh release view --repo "$REPO" --json tagName --jq .tagName 2>/dev/null || true)"
  [ -n "$VERSION" ] || fail "could not determine the latest release of $REPO. Is the tag pushed and the release published?"
fi

printf 'incoda: installing %s (%s) from %s\n' "$VERSION" "$ASSET" "$REPO"

WORK="$(mktemp -d)"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

gh release download "$VERSION" --repo "$REPO" --pattern "$ASSET" --pattern SHA256SUMS --dir "$WORK" \
  || fail "gh release download failed for $VERSION"

[ -f "$WORK/$ASSET" ]      || fail "the release does not contain $ASSET"
[ -f "$WORK/SHA256SUMS" ]  || fail 'the release does not contain SHA256SUMS; refusing to install an unverified binary'

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
