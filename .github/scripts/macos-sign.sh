#!/bin/sh
# Developer ID signing and notarization for the release workflow, on macOS.
#
#   macos-sign.sh setup           import the certificate into a temporary keychain
#   macos-sign.sh binary OS PATH  sign and notarize one sb binary (GoReleaser hook)
#   macos-sign.sh notarize PATH   notarize a .dmg/.app/.zip, stapling what can be
#   macos-sign.sh cleanup         delete the keychain and the API key
#
# setup reads the repository secrets APPLE_CERTIFICATE (base64 .p12),
# APPLE_CERTIFICATE_PASSWORD, APPLE_API_KEY_ID, APPLE_API_ISSUER_ID and
# APPLE_API_PRIVATE_KEY (the .p8 file's contents); see docs/releasing.md. It
# writes APPLE_SIGNING_IDENTITY, APPLE_API_KEY, APPLE_API_ISSUER and
# APPLE_API_KEY_PATH to $GITHUB_ENV, the names Tauri reads, so later steps and
# `tauri build` sign with the same keychain. Without the certificate, setup
# prints a notice and every other command does nothing: builds stay unsigned.
set -eu

tmp=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
keychain="$tmp/switchboard-signing.keychain-db"
api_key="$tmp/switchboard-notary-key.p8"
entitlements=$(cd "$(dirname "$0")/../.." && pwd)/app/tray/src-tauri/Entitlements.plist

die() {
	echo "::error::$*" >&2
	exit 1
}

need() { # NAME VALUE
	[ -n "$2" ] || die "APPLE_CERTIFICATE is set but $1 is not; set every macOS signing secret in docs/releasing.md, or none."
}

setup() {
	if [ -z "${APPLE_CERTIFICATE:-}" ]; then
		echo "::notice::APPLE_CERTIFICATE is not set; macOS builds are not signed or notarized."
		return 0
	fi
	need APPLE_CERTIFICATE_PASSWORD "${APPLE_CERTIFICATE_PASSWORD:-}"
	need APPLE_API_KEY_ID "${APPLE_API_KEY_ID:-}"
	need APPLE_API_ISSUER_ID "${APPLE_API_ISSUER_ID:-}"
	need APPLE_API_PRIVATE_KEY "${APPLE_API_PRIVATE_KEY:-}"
	[ -n "${GITHUB_ENV:-}" ] || die "setup must run in GitHub Actions (GITHUB_ENV is unset)"

	# A random password: the keychain lives only for this job.
	pass=$(openssl rand -base64 32)
	security create-keychain -p "$pass" "$keychain"
	security set-keychain-settings -lut 21600 "$keychain" # lock after 6 hours
	security unlock-keychain -p "$pass" "$keychain"

	# The Developer ID intermediates, in case the runner image lacks them;
	# codesign needs the full chain.
	for ca in DeveloperIDG2CA DeveloperIDCA; do
		curl -fsSL -o "$tmp/$ca.cer" "https://www.apple.com/certificateauthority/$ca.cer" &&
			security import "$tmp/$ca.cer" -k "$keychain" >/dev/null 2>&1 || true
		rm -f "$tmp/$ca.cer"
	done

	p12="$tmp/switchboard-signing.p12"
	(umask 077 && printf '%s' "$APPLE_CERTIFICATE" | base64 --decode >"$p12")
	if ! security import "$p12" -k "$keychain" -P "$APPLE_CERTIFICATE_PASSWORD" -f pkcs12 \
		-T /usr/bin/codesign -T /usr/bin/security >/dev/null; then
		rm -f "$p12"
		die "couldn't import APPLE_CERTIFICATE; check it is the base64 of the .p12 and that APPLE_CERTIFICATE_PASSWORD matches"
	fi
	rm -f "$p12"
	# Let codesign use the key without a UI prompt.
	security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$pass" "$keychain" >/dev/null

	# Search this keychain first, keeping the existing ones.
	# shellcheck disable=SC2046 # one keychain path per word
	security list-keychains -d user -s "$keychain" $(security list-keychains -d user | tr -d '"')

	identity=$(security find-identity -v -p codesigning "$keychain" |
		sed -n 's/^ *[0-9]*) \([0-9A-F]\{40\}\) "Developer ID Application: .*"$/\1/p' | head -n 1)
	[ -n "$identity" ] || die "APPLE_CERTIFICATE has no valid Developer ID Application identity; export that certificate (with its private key) as the .p12"
	echo "Signing with: $(security find-identity -v -p codesigning "$keychain" | grep "$identity" | sed 's/^[^"]*//')"

	(umask 077 && printf '%s\n' "$APPLE_API_PRIVATE_KEY" >"$api_key")
	{
		echo "APPLE_SIGNING_IDENTITY=$identity"
		echo "APPLE_API_KEY=$APPLE_API_KEY_ID"
		echo "APPLE_API_ISSUER=$APPLE_API_ISSUER_ID"
		echo "APPLE_API_KEY_PATH=$api_key"
	} >>"$GITHUB_ENV"
}

enabled() { [ -n "${APPLE_SIGNING_IDENTITY:-}" ]; }

# notarize PATH: submits PATH (zipping a bare binary or .app first), waits for
# the verdict, prints Apple's log if it is rejected, and staples a .dmg/.app.
notarize() {
	f=$1
	case "$f" in
	*.dmg | *.zip | *.pkg) upload=$f ;;
	*)
		upload="$tmp/notarize-$(basename "$f")-$$.zip"
		ditto -c -k --keepParent "$f" "$upload"
		;;
	esac
	result="$tmp/notarize-$$.json"
	echo "notarize: submitting $(basename "$f")"
	xcrun notarytool submit "$upload" --key "$APPLE_API_KEY_PATH" --key-id "$APPLE_API_KEY" \
		--issuer "$APPLE_API_ISSUER" --wait --timeout 30m --output-format json >"$result" ||
		{ cat "$result" >&2; die "notarytool couldn't submit $(basename "$f"); check the APPLE_API_* secrets"; }
	[ "$upload" = "$f" ] || rm -f "$upload"
	status=$(plutil -extract status raw -o - "$result")
	id=$(plutil -extract id raw -o - "$result")
	rm -f "$result"
	if [ "$status" != "Accepted" ]; then
		xcrun notarytool log "$id" --key "$APPLE_API_KEY_PATH" --key-id "$APPLE_API_KEY" \
			--issuer "$APPLE_API_ISSUER" >&2 || true
		die "notarization of $(basename "$f") was $status (submission $id); Apple's log is above"
	fi
	echo "notarize: $(basename "$f") accepted ($id)"
	case "$f" in
	# A bare binary can't hold a ticket; Gatekeeper looks it up online.
	*.dmg | *.app | *.pkg)
		xcrun stapler staple "$f"
		xcrun stapler validate "$f"
		;;
	esac
}

binary() {
	[ "$1" = darwin ] || return 0
	enabled || return 0
	codesign --force --timestamp --options runtime --entitlements "$entitlements" \
		--identifier dev.switchboard.sb --sign "$APPLE_SIGNING_IDENTITY" "$2"
	codesign --verify --strict --verbose=2 "$2"
	notarize "$2"
}

cleanup() {
	if [ -f "$keychain" ]; then
		security delete-keychain "$keychain" || rm -f "$keychain"
	fi
	rm -f "$api_key"
}

case "${1:-}" in
setup) setup ;;
binary)
	[ $# -eq 3 ] || die "usage: $0 binary OS PATH"
	binary "$2" "$3"
	;;
notarize)
	[ $# -eq 2 ] || die "usage: $0 notarize PATH"
	enabled || exit 0
	notarize "$2"
	;;
cleanup) cleanup ;;
*) die "usage: $0 setup | binary OS PATH | notarize PATH | cleanup" ;;
esac
