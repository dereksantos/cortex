#!/bin/sh
# Cortex installer — macOS and Linux, amd64 and arm64.
#
#   curl -fsSL https://raw.githubusercontent.com/dereksantos/cortex/main/scripts/install.sh | bash
#
# Downloads the latest GitHub release binary (checksum-verified when a
# sha256 tool is available) into $CORTEX_INSTALL_DIR (default ~/.local/bin).
# While no binary release is published yet, falls back to `go install`.
# No sudo, no config written, nothing outside the install dir is touched.
set -eu

REPO="dereksantos/cortex"
BIN_DIR="${CORTEX_INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin | linux) ;;
  *)
    echo "cortex install: unsupported OS '$os' — Cortex runs on macOS and Linux." >&2
    exit 1
    ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *)
    echo "cortex install: unsupported architecture '$arch' (amd64 and arm64 only)." >&2
    exit 1
    ;;
esac

go_install_fallback() {
  if command -v go >/dev/null 2>&1; then
    echo "cortex install: no published binary release yet — installing with 'go install' instead."
    go install "github.com/$REPO/cmd/cortex@latest"
    gobin=$(go env GOBIN)
    [ -n "$gobin" ] || gobin="$(go env GOPATH)/bin"
    echo "cortex install: done — $gobin/cortex"
    "$gobin/cortex" --version || true
    case ":$PATH:" in
      *":$gobin:"*) ;;
      *) echo "note: add $gobin to your PATH." ;;
    esac
    exit 0
  fi
  echo "cortex install: no published binary release yet, and no Go toolchain found." >&2
  echo "Install Go 1.26+ (https://go.dev/dl/) and re-run, or build from source:" >&2
  echo "  git clone https://github.com/$REPO.git && cd cortex && go build -o bin/cortex ./cmd/cortex" >&2
  exit 1
}

tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
  grep -m1 '"tag_name"' | cut -d'"' -f4 || true)
[ -n "$tag" ] || go_install_fallback

version=${tag#v}
name="cortex_${version}_${os}_${arch}"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "cortex install: fetching $name.tar.gz ($tag)"
curl -fsSL -o "$tmp/$name.tar.gz" "$base/$name.tar.gz"

if curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$tmp" && grep "[[:space:]]$name.tar.gz\$" checksums.txt | sha256sum -c - >/dev/null)
    echo "cortex install: checksum verified"
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$tmp" && grep "[[:space:]]$name.tar.gz\$" checksums.txt | shasum -a 256 -c - >/dev/null)
    echo "cortex install: checksum verified"
  else
    echo "cortex install: no sha256 tool found — skipping checksum verification"
  fi
fi

tar -xzf "$tmp/$name.tar.gz" -C "$tmp" cortex
mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/cortex" "$BIN_DIR/cortex"

echo "cortex install: done — $BIN_DIR/cortex"
"$BIN_DIR/cortex" --version || true
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "note: add $BIN_DIR to your PATH." ;;
esac
