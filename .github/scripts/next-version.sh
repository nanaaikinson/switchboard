#!/bin/sh
# Prints the next release tag for HEAD, worked out from the conventional
# commits since the last stable tag (vX.Y.Z; pre-releases are ignored), or
# nothing if none of them calls for a release:
#
#   feat:                         minor
#   fix: / perf:                  patch
#   feat!: or BREAKING CHANGE:    major, but minor before 1.0 (see docs/releasing.md)
#   anything else (docs, chore, ci, test, refactor, ...)   no release
#
# It also prints nothing if HEAD already has a stable tag.
set -eu

stable() { grep -E '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || true; }

if [ -n "$(git tag --points-at HEAD | stable)" ]; then
	exit 0
fi

last=$(git tag --list 'v*' | stable | sort -t. -k1.2,1n -k2,2n -k3,3n | tail -n 1)
if [ -n "$last" ]; then
	range="$last..HEAD"
else
	range=HEAD
	last=v0.0.0
fi

# 3 = breaking, 2 = feature, 1 = fix, 0 = no release. Commits are separated
# by the ASCII record separator so bodies can span lines.
level=$(git log --format='%x1e%s%n%b' "$range" | awk '
	BEGIN { RS = "\036"; max = 0 }
	NF == 0 { next }
	{
		split($0, lines, "\n"); subject = lines[1]; l = 0
		if (subject ~ /^[a-zA-Z]+(\([^)]*\))?!:/) l = 3
		else if (subject ~ /^feat(\([^)]*\))?:/) l = 2
		else if (subject ~ /^(fix|perf)(\([^)]*\))?:/) l = 1
		for (i = 2; i in lines; i++) if (lines[i] ~ /^BREAKING[ -]CHANGE:/) l = 3
		if (l > max) max = l
	}
	END { print max }')

v=${last#v}
major=${v%%.*}
rest=${v#*.}
minor=${rest%%.*}
patch=${rest#*.}

case "$level" in
3)
	if [ "$major" -eq 0 ]; then
		minor=$((minor + 1)) patch=0
	else
		major=$((major + 1)) minor=0 patch=0
	fi
	;;
2) minor=$((minor + 1)) patch=0 ;;
1) patch=$((patch + 1)) ;;
*) exit 0 ;;
esac
echo "v$major.$minor.$patch"
