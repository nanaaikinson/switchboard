#!/bin/sh
# Checks files downloaded from a GitHub release against what the release job
# built, before anything signs them: SHA256SUMS must carry the release key's
# signature for this tag (SHA256SUMS.minisig, trusted comment
# "switchboard <tag> SHA256SUMS"), and every FILE must be listed in it with a
# matching SHA-256. Anyone who can edit the release's assets can't have their
# files signed this way. Needs minisign.
#
#   verify-release-sums.sh TAG PUBLIC_KEY SHA256SUMS FILE...
#
# e.g. verify-release-sums.sh v0.3.0 RW... dist/SHA256SUMS dist/sb_*.tar.gz dist/sb_*.zip
# SHA256SUMS.minisig is read from next to SHA256SUMS.
set -eu

[ $# -ge 4 ] || {
	echo "usage: $0 TAG PUBLIC_KEY SHA256SUMS FILE..." >&2
	exit 2
}
tag=$1 key=$2 sums=$3
shift 3

fail() {
	echo "::error::$*" >&2
	exit 1
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

# -Q prints only the trusted comment.
comment=$(minisign -V -Q -m "$sums" -x "$sums.minisig" -P "$key") ||
	fail "SHA256SUMS doesn't verify against SHA256SUMS.minisig with the release key; not signing anything"
want="switchboard $tag SHA256SUMS"
[ "$comment" = "$want" ] ||
	fail "SHA256SUMS.minisig is signed as '$comment', not '$want'; not signing anything"

for f in "$@"; do
	name=$(basename "$f")
	[ -f "$f" ] || fail "$f doesn't exist"
	sum=$(awk -v f="$name" '$2 == f || $2 == "*" f { print $1; exit }' "$sums")
	[ -n "$sum" ] || fail "$name is not in SHA256SUMS, so the release job didn't build it; not signing it"
	got=$(sha256 "$f")
	[ "$got" = "$sum" ] ||
		fail "$name has SHA-256 $got, but SHA256SUMS says $sum: it changed after the release job built it; not signing it"
	echo "verified $name"
done
