#!/bin/sh
# Writes the winget manifests for one release of sb: the portable sb.exe from
# the Windows zips, as package Switchboard.Switchboard with the command `sb`.
#
#   winget-manifests.sh TAG REPO SHA256SUMS OUTDIR
#
# e.g. winget-manifests.sh v0.3.0 nanaaikinson/switchboard dist/SHA256SUMS out
# The release workflow submits OUTDIR with `wingetcreate submit`. The manifests
# are written in full each time, because `wingetcreate update` would keep the
# previous version's path inside the zip (sb_<version>_windows_<arch>\sb.exe).
set -eu

[ $# -eq 4 ] || {
	echo "usage: $0 TAG REPO SHA256SUMS OUTDIR" >&2
	exit 2
}
tag=$1 repo=$2 sums=$3 out=$4
version=${tag#v}
id=Switchboard.Switchboard
schema=1.9.0
base="https://github.com/$repo/releases/download/$tag"

sha() { # FILE: its SHA-256 from SHA256SUMS, upper-case as winget-pkgs writes it
	h=$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$sums")
	[ -n "$h" ] || {
		echo "winget-manifests: $1 is not in $sums" >&2
		exit 1
	}
	echo "$h" | tr 'a-f' 'A-F'
}

installer() { # WINGET_ARCH GOARCH
	dir="sb_${version}_windows_$2"
	hash=$(sha "$dir.zip") # a plain assignment, so a missing hash stops the script
	cat <<EOF
- Architecture: $1
  InstallerUrl: $base/$dir.zip
  InstallerSha256: $hash
  NestedInstallerFiles:
  - RelativeFilePath: $dir\\sb.exe
    PortableCommandAlias: sb
EOF
}

mkdir -p "$out"
header="# yaml-language-server: \$schema=https://aka.ms/winget-manifest"

cat >"$out/$id.yaml" <<EOF
$header.version.$schema.schema.json
PackageIdentifier: $id
PackageVersion: $version
DefaultLocale: en-US
ManifestType: version
ManifestVersion: $schema
EOF

{
	cat <<EOF
$header.installer.$schema.schema.json
PackageIdentifier: $id
PackageVersion: $version
InstallerType: zip
NestedInstallerType: portable
Commands:
- sb
ReleaseDate: $(date -u +%Y-%m-%d)
Installers:
EOF
	installer x64 amd64
	installer arm64 arm64
	cat <<EOF
ManifestType: installer
ManifestVersion: $schema
EOF
} >"$out/$id.installer.yaml"

cat >"$out/$id.locale.en-US.yaml" <<EOF
$header.defaultLocale.$schema.schema.json
PackageIdentifier: $id
PackageVersion: $version
PackageLocale: en-US
Publisher: Nana Kwesi Ofosu-Aikins
PublisherUrl: https://github.com/${repo%%/*}
PackageName: Switchboard
PackageUrl: https://github.com/$repo
License: Proprietary
ShortDescription: Map local ports to trusted HTTPS names
Description: Switchboard routes names such as https://myapp.test to apps on local ports, with a local DNS server, a reverse proxy and a name-constrained local CA. Run 'sb setup' once after installing, and 'sb uninstall' before uninstalling to undo its system changes.
Moniker: switchboard
Tags:
- dns
- https
- localhost
- proxy
- tls
ReleaseNotesUrl: https://github.com/$repo/releases/tag/$tag
ManifestType: defaultLocale
ManifestVersion: $schema
EOF
