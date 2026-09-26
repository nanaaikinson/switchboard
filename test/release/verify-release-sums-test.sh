#!/bin/sh
# Tests .github/scripts/verify-release-sums.sh with made-up release files and
# a stand-in minisign.
set -eu

script=$(cd "$(dirname "$0")/../.." && pwd)/.github/scripts/verify-release-sums.sh
failures=0
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

check() { # DESCRIPTION COMMAND...
	desc=$1
	shift
	if "$@"; then
		echo "ok   $desc"
	else
		echo "FAIL $desc"
		failures=$((failures + 1))
	fi
}

# minisign "verifies" with the release key, printing $FAKE_COMMENT as the
# trusted comment, or fails if FAKE_BAD_SIGNATURE=1. It comes first on PATH.
mkdir -p "$dir/bin"
cat >"$dir/bin/minisign" <<'EOF'
#!/bin/sh
case " $* " in *" -V -Q "*" -P RWrelease-key "*) ;; *) echo "unexpected arguments: $*" >&2; exit 2 ;; esac
[ "${FAKE_BAD_SIGNATURE:-}" != 1 ] || exit 1
printf '%s\n' "${FAKE_COMMENT:-switchboard v0.3.0 SHA256SUMS}"
EOF
chmod +x "$dir/bin/minisign"
PATH="$dir/bin:$PATH"
export PATH

sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d ' ' -f 1; }
mkdir -p "$dir/dist"
printf 'darwin\n' >"$dir/dist/sb_0.3.0_darwin_arm64.tar.gz"
printf 'windows\n' >"$dir/dist/sb_0.3.0_windows_amd64.zip"
{
	printf '%s  sb_0.3.0_darwin_arm64.tar.gz\n' "$(sha "$dir/dist/sb_0.3.0_darwin_arm64.tar.gz")"
	printf '%s  sb_0.3.0_windows_amd64.zip\n' "$(sha "$dir/dist/sb_0.3.0_windows_amd64.zip")"
} >"$dir/dist/SHA256SUMS"
: >"$dir/dist/SHA256SUMS.minisig"

verify() { sh "$script" v0.3.0 RWrelease-key "$dir/dist/SHA256SUMS" "$@" >"$dir/out" 2>&1; }
verify_with() { # VAR=value FILE...: verify with a variable set for minisign
	v=$1
	shift
	env "$v" sh "$script" v0.3.0 RWrelease-key "$dir/dist/SHA256SUMS" "$@" >"$dir/out" 2>&1
}
says() { grep -qF -- "$1" "$dir/out"; }
not() { ! "$@"; }

check "accepts the release job's files" verify "$dir"/dist/sb_*
check "says what it verified" says "verified sb_0.3.0_windows_amd64.zip"

check "refuses a bad SHA256SUMS signature" not verify_with FAKE_BAD_SIGNATURE=1 "$dir"/dist/sb_*
check "says the signature is bad" says "SHA256SUMS doesn't verify against SHA256SUMS.minisig"
check "refuses another release's SHA256SUMS" not verify_with FAKE_COMMENT="switchboard v0.2.0 SHA256SUMS" "$dir"/dist/sb_*
check "says whose it is" says "signed as 'switchboard v0.2.0 SHA256SUMS', not 'switchboard v0.3.0 SHA256SUMS'"

cp "$dir/dist/sb_0.3.0_darwin_arm64.tar.gz" "$dir/orig"
printf 'swapped\n' >"$dir/dist/sb_0.3.0_darwin_arm64.tar.gz"
check "refuses an archive changed after the release job" not verify "$dir"/dist/sb_*
check "says which archive changed" says "sb_0.3.0_darwin_arm64.tar.gz has SHA-256"
cp "$dir/orig" "$dir/dist/sb_0.3.0_darwin_arm64.tar.gz"

printf 'extra\n' >"$dir/dist/sb_0.3.0_linux_amd64.tar.gz"
check "refuses an archive the release job didn't build" not verify "$dir"/dist/sb_*
check "says it isn't in SHA256SUMS" says "sb_0.3.0_linux_amd64.tar.gz is not in SHA256SUMS"
rm "$dir/dist/sb_0.3.0_linux_amd64.tar.gz"

check "refuses a missing file" not verify "$dir/dist/sb_0.3.0_darwin_amd64.tar.gz"
usage() { ! sh "$script" v0.3.0 RWrelease-key "$dir/dist/SHA256SUMS" 2>/dev/null; }
check "needs at least one file" usage

[ "$failures" -eq 0 ] || {
	echo "$failures failed"
	exit 1
}
