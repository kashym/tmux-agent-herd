#!/usr/bin/env bash
# Install bin/herd: download the release asset for this OS/arch, or fall
# back to `go build` from source. Idempotent; skips if the version matches.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(cat "$ROOT/VERSION")"
BIN="$ROOT/bin/herd"
# GitHub "owner/repo" to download releases from (override with HERD_REPO).
REPO="${HERD_REPO:-kashym/tmux-agent-herd}"

if [ -x "$BIN" ] && [ "$("$BIN" version 2>/dev/null)" = "herd $VERSION" ]; then
	exit 0
fi
mkdir -p "$ROOT/bin"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
esac

download() {
	[ -n "$REPO" ] || return 1
	local url="https://github.com/$REPO/releases/download/v$VERSION/herd_${VERSION}_${os}_${arch}.tar.gz"
	local tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' RETURN
	if command -v curl >/dev/null; then
		curl -fsSL "$url" -o "$tmp/herd.tgz" || return 1
	elif command -v wget >/dev/null; then
		wget -q "$url" -O "$tmp/herd.tgz" || return 1
	else
		return 1
	fi
	tar -xzf "$tmp/herd.tgz" -C "$tmp" herd || return 1
	install -m 0755 "$tmp/herd" "$BIN"
}

build() {
	local go
	go="$(command -v go || true)"
	if [ -z "$go" ] && command -v mise >/dev/null; then
		go="$(mise which go 2>/dev/null || true)"
	fi
	[ -n "$go" ] || {
		echo "tmux-agent-herd: no release for $os/$arch and Go is not installed" >&2
		return 1
	}
	(cd "$ROOT" && CGO_ENABLED=0 "$go" build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$BIN" ./cmd/herd)
}

download || build
"$BIN" version >/dev/null
