#!/bin/sh
# Tests .github/scripts/winget-manifests.sh against a made-up SHA256SUMS.
set -eu

script=$(cd "$(dirname "$0")/../.." && pwd)/.github/scripts/winget-manifests.sh
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

cat >"$dir/SHA256SUMS" <<'SUMS'
aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  sb_0.3.0_windows_amd64.zip
bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  sb_0.3.0_windows_arm64.zip
cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc  sb_0.3.0_darwin_arm64.tar.gz
SUMS

sh "$script" v0.3.0 owner/switchboard "$dir/SHA256SUMS" "$dir/out"
inst="$dir/out/Switchboard.Switchboard.installer.yaml"

for m in Switchboard.Switchboard.yaml Switchboard.Switchboard.installer.yaml Switchboard.Switchboard.locale.en-US.yaml; do
	check "writes $m" test -s "$dir/out/$m"
done
check "x64 hash, upper-case" grep -q "InstallerSha256: $(printf '%064d' 0 | tr 0 A)" "$inst"
check "arm64 hash" grep -q "InstallerSha256: $(printf '%064d' 0 | tr 0 B)" "$inst"
check "x64 url" grep -q "InstallerUrl: https://github.com/owner/switchboard/releases/download/v0.3.0/sb_0.3.0_windows_amd64.zip" "$inst"
check "path inside the zip has this version" grep -qF 'RelativeFilePath: sb_0.3.0_windows_amd64\sb.exe' "$inst"
check "version manifest" grep -q "PackageVersion: 0.3.0" "$dir/out/Switchboard.Switchboard.yaml"
check "release notes link" grep -q "releases/tag/v0.3.0" "$dir/out/Switchboard.Switchboard.locale.en-US.yaml"
missing_zip_fails() { ! sh "$script" v9.9.9 o/r "$dir/SHA256SUMS" "$dir/missing" 2>/dev/null; }
check "fails when a zip is missing" missing_zip_fails

[ "$failures" -eq 0 ] || {
	echo "$failures failed"
	exit 1
}
