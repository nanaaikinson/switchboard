#!/bin/sh
# Builds the sb binary that the tray app ships as a Tauri sidecar, named
# src-tauri/binaries/sb-<target triple>[.exe] as Tauri expects. The triple is
# TAURI_ENV_TARGET_TRIPLE (set by `tauri build --target ...`), else the host's.
set -eu

here=$(cd "$(dirname "$0")/.." && pwd)
root=$(cd "$here/../.." && pwd)
triple=${TAURI_ENV_TARGET_TRIPLE:-$(rustc -vV | sed -n 's/^host: //p')}
version=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)
out="$here/src-tauri/binaries"
mkdir -p "$out"

build() { # GOOS GOARCH OUTPUT
	echo "sidecar: sb $version for $1/$2"
	(cd "$root" && CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$3" ./cmd/sb)
}

case "$triple" in
aarch64-apple-darwin) build darwin arm64 "$out/sb-$triple" ;;
x86_64-apple-darwin) build darwin amd64 "$out/sb-$triple" ;;
universal-apple-darwin)
	# tauri build --target universal-apple-darwin compiles each architecture
	# on its own, and each pass needs its own sidecar; the bundle gets the
	# universal one.
	build darwin arm64 "$out/sb-aarch64-apple-darwin"
	build darwin amd64 "$out/sb-x86_64-apple-darwin"
	lipo -create -output "$out/sb-$triple" "$out/sb-aarch64-apple-darwin" "$out/sb-x86_64-apple-darwin"
	;;
x86_64-pc-windows-msvc) build windows amd64 "$out/sb-$triple.exe" ;;
aarch64-pc-windows-msvc) build windows arm64 "$out/sb-$triple.exe" ;;
x86_64-unknown-linux-gnu) build linux amd64 "$out/sb-$triple" ;;
aarch64-unknown-linux-gnu) build linux arm64 "$out/sb-$triple" ;;
*)
	echo "sidecar: unsupported target $triple" >&2
	exit 1
	;;
esac
