#!/bin/sh
# Install snip from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/wilchen558/snip/main/install.sh | sh
#
# Environment:
#   SNIP_VERSION  tag to install (default: the latest release)
#   SNIP_BIN_DIR  install directory (default: ~/.local/bin)
set -eu

REPO=wilchen558/snip
BIN_DIR=${SNIP_BIN_DIR:-$HOME/.local/bin}

die() { printf 'install: %s\n' "$1" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need uname
need mktemp
need tar
if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1"; }
    fetch_to() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO- "$1"; }
    fetch_to() { wget -qO "$2" "$1"; }
else
    die "curl or wget is required"
fi

case $(uname -s) in
    Linux)  os=linux ;;
    Darwin) os=darwin ;;
    *)      die "unsupported OS $(uname -s); build from source with: go install github.com/$REPO@latest" ;;
esac

case $(uname -m) in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *)             die "unsupported architecture $(uname -m); build from source with: go install github.com/$REPO@latest" ;;
esac

version=${SNIP_VERSION:-}
if [ -z "$version" ]; then
    # Resolve the latest tag without needing jq.
    version=$(fetch "https://api.github.com/repos/$REPO/releases/latest" \
        | sed -n 's/.*"tag_name" *: *"\([^"]*\)".*/\1/p' | head -n1)
    [ -n "$version" ] || die "could not determine the latest release; set SNIP_VERSION"
fi

archive="snip_${version#v}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'downloading snip %s (%s/%s)\n' "$version" "$os" "$arch"
fetch_to "$base/$archive" "$tmp/$archive" || die "no asset $archive in release $version"

# Verify the checksum when a hasher is available; a failure here is fatal.
if fetch_to "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
    if command -v sha256sum >/dev/null 2>&1; then
        hash=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
    elif command -v shasum >/dev/null 2>&1; then
        hash=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
    fi
    if [ -n "${hash:-}" ]; then
        grep -q "^$hash  $archive$" "$tmp/checksums.txt" || die "checksum mismatch for $archive"
        printf 'checksum ok\n'
    fi
fi

tar -xzf "$tmp/$archive" -C "$tmp" snip || die "archive did not contain snip"

mkdir -p "$BIN_DIR" || die "cannot create $BIN_DIR"
install -m 0755 "$tmp/snip" "$BIN_DIR/snip" 2>/dev/null \
    || { cp "$tmp/snip" "$BIN_DIR/snip" && chmod 0755 "$BIN_DIR/snip"; } \
    || die "cannot write to $BIN_DIR; set SNIP_BIN_DIR to a writable directory"

printf 'installed %s to %s\n' "$("$BIN_DIR/snip" --version)" "$BIN_DIR/snip"

case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) printf '\n%s is not on your PATH. Add:\n  export PATH="%s:$PATH"\n' "$BIN_DIR" "$BIN_DIR" ;;
esac
