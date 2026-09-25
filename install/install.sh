#!/bin/sh
# Install sb, the Switchboard CLI, from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --global     # /usr/local/bin instead of ~/.local/bin
#   curl -fsSL .../install.sh | SB_VERSION=v0.1.0 sh  # pin a version
#
# The archive is checked against the release's SHA256SUMS. If minisign is
# installed, SHA256SUMS is also checked against its signature. This script
# only makes system changes (sudo) for --global when /usr/local/bin is not
# writable; everything else is done by 'sb setup' afterwards.
set -eu

REPO="nanaaikinson/switchboard"
# Public key that signs SHA256SUMS (SHA256SUMS.minisig). Empty until releases
# are signed; the signature check is skipped while it is empty.
MINISIGN_PUBKEY=""

say() { printf 'sb-install: %s\n' "$*"; }
warn() { printf 'sb-install: warning: %s\n' "$*" >&2; }
die() {
	printf 'sb-install: error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: install.sh [--global]

Install sb from GitHub Releases into ~/.local/bin.

Options:
  --global   install into /usr/local/bin (uses sudo if it is not writable)
  -h, --help show this help

Environment:
  SB_VERSION  release tag to install, e.g. v0.1.0 (default: latest release)
EOF
}

have() { command -v "$1" >/dev/null 2>&1; }

# download URL FILE
download() {
	if have curl; then
		curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$2" "$1"
	elif have wget; then
		wget -q --https-only -O "$2" "$1"
	else
		die "need curl or wget to download sb; install one and re-run"
	fi
}

detect_os() {
	case "$(uname -s)" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	MINGW* | MSYS* | CYGWIN*) die "on Windows, download the .zip from https://github.com/$REPO/releases" ;;
	*) die "unsupported OS $(uname -s); see https://github.com/$REPO/releases" ;;
	esac
}

detect_arch() {
	arch=$(uname -m)
	# An x86_64 shell under Rosetta on Apple silicon should still get arm64.
	if [ "$arch" = x86_64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
		arch=arm64
	fi
	case "$arch" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64) echo arm64 ;;
	*) die "unsupported CPU architecture $arch; see https://github.com/$REPO/releases" ;;
	esac
}

# latest_version prints the tag of the latest (non-prerelease) release.
latest_version() {
	json="$tmp/latest.json"
	download "https://api.github.com/repos/$REPO/releases/latest" "$json" ||
		die "could not look up the latest release; set SB_VERSION=vX.Y.Z to pick one"
	tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$json" | head -n 1)
	[ -n "$tag" ] || die "no release found; set SB_VERSION=vX.Y.Z to pick one"
	echo "$tag"
}

# sha256 FILE prints the file's SHA-256 in hex.
sha256() {
	if have sha256sum; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		die "need sha256sum or shasum to verify the download"
	fi
}

verify_signature() {
	if ! have minisign; then
		say "minisign not found; skipping signature check (checksum is still verified)"
		return
	fi
	if [ -z "$MINISIGN_PUBKEY" ]; then
		say "releases are not signed yet; skipping signature check (checksum is still verified)"
		return
	fi
	download "$base/SHA256SUMS.minisig" "$tmp/SHA256SUMS.minisig" ||
		die "could not download SHA256SUMS.minisig for $version; refusing to install unsigned files"
	minisign -Vqm "$tmp/SHA256SUMS" -x "$tmp/SHA256SUMS.minisig" -P "$MINISIGN_PUBKEY" ||
		die "SHA256SUMS signature is invalid; the release may have been tampered with. Do not install it"
	say "verified SHA256SUMS signature"
}

main() {
	global=0
	for arg in "$@"; do
		case "$arg" in
		--global) global=1 ;;
		-h | --help)
			usage
			exit 0
			;;
		*) die "unknown option $arg; see --help" ;;
		esac
	done

	os=$(detect_os)
	arch=$(detect_arch)

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t sb-install)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	version=${SB_VERSION:-}
	if [ -z "$version" ]; then
		version=$(latest_version)
	fi
	case "$version" in
	v*) ;;
	*) version="v$version" ;;
	esac
	case "$version" in
	*[!0-9A-Za-z.+-]* | v | v[!0-9]*) die "SB_VERSION=${SB_VERSION:-} is not a release tag like v0.1.0" ;;
	esac

	name="sb_${version#v}_${os}_${arch}"
	asset="$name.tar.gz"
	base="https://github.com/$REPO/releases/download/$version"

	say "downloading sb $version for $os/$arch"
	download "$base/$asset" "$tmp/$asset" ||
		die "could not download $asset; check that release $version exists at https://github.com/$REPO/releases"
	download "$base/SHA256SUMS" "$tmp/SHA256SUMS" ||
		die "could not download SHA256SUMS for $version; refusing to install unverified files"

	verify_signature

	want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/SHA256SUMS")
	[ -n "$want" ] || die "$asset is not listed in SHA256SUMS; refusing to install unverified files"
	got=$(sha256 "$tmp/$asset")
	[ "$got" = "$want" ] || die "checksum mismatch for $asset (got $got, want $want); the download is corrupt or was tampered with"
	say "verified checksum"

	tar -xzf "$tmp/$asset" -C "$tmp"
	[ -f "$tmp/$name/sb" ] || die "$asset does not contain $name/sb"

	if [ "$global" = 1 ]; then
		dir=/usr/local/bin
	else
		[ -n "${HOME:-}" ] || die "HOME is not set; use --global or set HOME"
		dir="$HOME/.local/bin"
	fi
	sudo=""
	if [ "$global" = 1 ] && ! { mkdir -p "$dir" 2>/dev/null && [ -w "$dir" ]; }; then
		have sudo || die "$dir is not writable and sudo is not available"
		say "$dir is not writable; using sudo to install there"
		sudo=sudo
	fi
	$sudo mkdir -p "$dir"
	# Copy next to the target, then rename, so a running sb is never half-written.
	$sudo cp "$tmp/$name/sb" "$dir/.sb.tmp.$$"
	$sudo chmod 0755 "$dir/.sb.tmp.$$"
	$sudo mv -f "$dir/.sb.tmp.$$" "$dir/sb"
	say "installed sb $version to $dir/sb"

	case ":${PATH:-}:" in
	*":$dir:"*) sb="sb" ;;
	*)
		sb="$dir/sb"
		warn "$dir is not on your PATH. Add this line to your shell profile (~/.zshrc, ~/.bashrc):"
		printf '\n    export PATH="%s:%sPATH"\n\n' "$dir" "\$" >&2
		;;
	esac

	say "next: run '$sb setup' to finish (asks for your password once). Re-run it after every upgrade."
}

main "$@"
