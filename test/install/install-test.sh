#!/bin/sh
# Tests install/install.sh against a fake release. Downloads come from a local
# folder through a stand-in curl, minisign is a stand-in too, and sb is
# installed under a temporary HOME. PATH holds only the tools the script
# needs, so a real minisign on this machine doesn't change the results.
#
#   sh test/install/install-test.sh
set -eu

script=$(cd "$(dirname "$0")/../.." && pwd)/install/install.sh
failures=0
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
release="$dir/release"
bin="$dir/bin"
mkdir -p "$release" "$bin"

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

# A release v1.2.3 for every OS and CPU install.sh knows, and its SHA256SUMS.
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d ' ' -f 1; }
for os in darwin linux; do
	for arch in amd64 arm64; do
		name="sb_1.2.3_${os}_${arch}"
		mkdir -p "$dir/stage/$name"
		printf 'sb 1.2.3\n' >"$dir/stage/$name/sb"
		tar -czf "$release/$name.tar.gz" -C "$dir/stage" "$name"
		printf '%s  %s\n' "$(sha "$release/$name.tar.gz")" "$name.tar.gz" >>"$release/SHA256SUMS"
	done
done
printf 'untrusted comment: fake\n' >"$release/SHA256SUMS.minisig"

# The tools install.sh uses, and nothing else.
for tool in sh mktemp rm uname sysctl sed head cut awk tar gzip sha256sum shasum cp chmod mv mkdir cat; do
	if p=$(command -v "$tool"); then ln -s "$p" "$bin/$tool"; fi
done

# curl serves the fake release, and logs its arguments.
cat >"$bin/curl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>"$dir/curl.log"
out=""; url=""
while [ \$# -gt 0 ]; do
	case "\$1" in
	-o) out=\$2; shift ;;
	https://github.com/nanaaikinson/switchboard/releases/download/v1.2.3/*) url=\$1 ;;
	esac
	shift
done
src="$release/\${url##*/}"
[ -n "\$url" ] && [ -f "\$src" ] || exit 22
cp "\$src" "\$out"
EOF
chmod +x "$bin/curl"

# minisign, when present, "verifies" with the pinned key, printing
# \$FAKE_COMMENT as the trusted comment, or fails if FAKE_BAD_SIGNATURE=1.
cat >"$dir/minisign" <<'EOF'
#!/bin/sh
case " $* " in *" -V -Q "*" -P RWfake-release-key "*) ;; *) echo "unexpected arguments: $*" >&2; exit 2 ;; esac
[ "${FAKE_BAD_SIGNATURE:-}" != 1 ] || exit 1
printf '%s\n' "$FAKE_COMMENT"
EOF
chmod +x "$dir/minisign"
with_minisign() { ln -sf "$dir/minisign" "$bin/minisign"; }
without_minisign() { rm -f "$bin/minisign"; }

# install.sh with the test key pinned in place of the real one, and with no
# key, as it was before the release key existed.
sed 's/^MINISIGN_PUBKEY=".*"$/MINISIGN_PUBKEY="RWfake-release-key"/' "$script" >"$dir/pinned.sh"
sed 's/^MINISIGN_PUBKEY=".*"$/MINISIGN_PUBKEY=""/' "$script" >"$dir/unpinned.sh"
check "the key slot is where the tests expect it" grep -q '^MINISIGN_PUBKEY="RWfake-release-key"$' "$dir/pinned.sh"
check "install.sh pins a release key" grep -q '^MINISIGN_PUBKEY="RW[A-Za-z0-9+/]\{54\}"$' "$script"

# run SCRIPT [VAR=value...]: installs v1.2.3 into a fresh HOME; the output is
# in $dir/out.
run() {
	s=$1
	shift
	rm -rf "${dir:?}/home" "$dir/curl.log"
	mkdir -p "$dir/home"
	env -i HOME="$dir/home" PATH="$bin" SB_VERSION=v1.2.3 FAKE_COMMENT="switchboard v1.2.3 SHA256SUMS" "$@" \
		sh "$s" >"$dir/out" 2>&1
}
not() { ! "$@"; }
installed() { [ -f "$dir/home/.local/bin/sb" ]; }
not_installed() { ! installed; }
says() { grep -qF -- "$1" "$dir/out"; }

without_minisign
check "no key: installs on the checksum" run "$dir/unpinned.sh"
check "no key: sb is installed" installed
check "no key: warns that the release is not signature-verified" says "warning: this release is NOT signature-verified"
check "curl is limited to HTTPS, redirects included" grep -q -- "--proto =https --proto-redir =https --tlsv1.2" "$dir/curl.log"

check "pinned key, no minisign: refuses" not run "$dir/pinned.sh"
check "pinned key, no minisign: says how to get minisign" says "minisign is needed to verify the release signature. Install it (brew install minisign"
check "pinned key, no minisign: nothing installed" not_installed

check "pinned key, no minisign, opted out: installs" run "$dir/pinned.sh" SB_INSECURE_SKIP_SIGNATURE=1
check "pinned key, no minisign, opted out: warns loudly" says "warning: SB_INSECURE_SKIP_SIGNATURE=1: NOT checking the release signature"
check "pinned key, no minisign, opted out: sb is installed" installed

with_minisign
check "pinned key, valid signature: installs" run "$dir/pinned.sh"
check "pinned key, valid signature: says so" says "verified SHA256SUMS signature"
check "pinned key, valid signature: sb is installed" installed

check "pinned key, invalid signature: refuses" not run "$dir/pinned.sh" FAKE_BAD_SIGNATURE=1
check "pinned key, invalid signature: says why" says "SHA256SUMS signature is invalid"
check "pinned key, invalid signature: nothing installed" not_installed

check "pinned key, another release's signature: refuses" not run "$dir/pinned.sh" FAKE_COMMENT="switchboard v1.0.0 SHA256SUMS"
check "pinned key, another release's signature: says why" says "signed as 'switchboard v1.0.0 SHA256SUMS', not 'switchboard v1.2.3 SHA256SUMS'"
check "pinned key, another release's signature: nothing installed" not_installed

check "pinned key, opting out doesn't skip a check minisign can do" not run "$dir/pinned.sh" SB_INSECURE_SKIP_SIGNATURE=1 FAKE_BAD_SIGNATURE=1

mv "$release/SHA256SUMS.minisig" "$dir/minisig.bak"
check "pinned key, unsigned release: refuses" not run "$dir/pinned.sh"
check "pinned key, unsigned release: says why" says "could not download SHA256SUMS.minisig"
mv "$dir/minisig.bak" "$release/SHA256SUMS.minisig"

cp "$release/SHA256SUMS" "$dir/sums.bak"
sed 's/^[0-9a-f]\{64\}/0000000000000000000000000000000000000000000000000000000000000000/' "$dir/sums.bak" >"$release/SHA256SUMS"
check "checksum mismatch: refuses" not run "$dir/pinned.sh"
check "checksum mismatch: says why" says "checksum mismatch"
cp "$dir/sums.bak" "$release/SHA256SUMS"

[ "$failures" -eq 0 ] || {
	echo "$failures failed"
	exit 1
}
