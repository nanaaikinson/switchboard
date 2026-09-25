#!/bin/sh
# Tests .github/scripts/next-version.sh against throwaway git repositories.
set -eu

script=$(cd "$(dirname "$0")/../.." && pwd)/.github/scripts/next-version.sh
failures=0

repo() {
	dir=$(mktemp -d)
	git -C "$dir" init -q -b main
	git -C "$dir" config user.email test@example.com
	git -C "$dir" config user.name test
	git -C "$dir" config tag.gpgSign false
	git -C "$dir" config commit.gpgSign false
	echo "$dir"
}

commit() { git -C "$1" commit -q --allow-empty -m "$2"; }

expect() {
	got=$(cd "$1" && sh "$script")
	if [ "$got" = "$2" ]; then
		echo "ok   $3"
	else
		echo "FAIL $3: got '$got', want '$2'"
		failures=$((failures + 1))
	fi
	rm -rf "$1"
}

r=$(repo); commit "$r" "chore: init"; commit "$r" "feat: first"
expect "$r" v0.1.0 "first feature without tags"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v0.1.0-rc.2; commit "$r" "fix(cli): b"
expect "$r" v0.1.0 "pre-release tags are ignored"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v0.1.0; commit "$r" "fix: b"; commit "$r" "docs: c"
expect "$r" v0.1.1 "fix bumps patch"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v0.1.0; commit "$r" "fix: b"; commit "$r" "feat(docker): c"
expect "$r" v0.2.0 "feat bumps minor and resets patch"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v0.1.0; commit "$r" "docs: b"; commit "$r" "chore: c"; commit "$r" "ci: d"
expect "$r" "" "no release for docs/chore/ci only"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v0.1.0; commit "$r" "feat(api)!: drop v0 fields"
expect "$r" v0.2.0 "breaking change before 1.0 bumps minor"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v1.4.2; commit "$r" "refactor: x

BREAKING CHANGE: config key renamed"
expect "$r" v2.0.0 "BREAKING CHANGE footer after 1.0 bumps major"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v0.9.0; git -C "$r" tag v0.10.0; commit "$r" "fix: b"
expect "$r" v0.10.1 "tags compare numerically, not as text"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v0.3.0
expect "$r" "" "HEAD already released"

r=$(repo); commit "$r" "feat: a"; git -C "$r" tag v0.3.0; commit "$r" "Merge pull request #9 from x/feat-y"; commit "$r" "featuring: not a type"
expect "$r" "" "non-conventional subjects don't release"

[ "$failures" -eq 0 ] || { echo "$failures failure(s)"; exit 1; }
