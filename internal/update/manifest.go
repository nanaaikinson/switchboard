package update

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// Manifest is an update channel's JSON document,
// e.g. https://nanaaikinson.github.io/switchboard-updates/stable.json.
type Manifest struct {
	Version string `json:"version"`
	Notes   string `json:"notes,omitempty"`
	PubDate string `json:"pub_date,omitempty"`
	// RolloutPercent is the share of installs (0-100) that get this release;
	// absent means 100.
	RolloutPercent *int `json:"rollout_percent,omitempty"`
	// Platforms is keyed by GOOS-GOARCH, e.g. "darwin-arm64".
	Platforms map[string]Asset `json:"platforms"`
}

// Asset is one platform's release archive.
type Asset struct {
	URL    string `json:"url"`    // the .tar.gz (.zip on Windows) from the release
	SHA256 string `json:"sha256"` // hex
	// Signature is the archive's minisign signature file, base64-encoded (the
	// Tauri updater's convention). Its trusted comment must be
	// "sb <version> <platform>".
	Signature string `json:"signature"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Rollout is RolloutPercent, defaulting to 100.
func (m Manifest) Rollout() int {
	if m.RolloutPercent == nil {
		return 100
	}
	return *m.RolloutPercent
}

// Validate checks the manifest's shape, not its signatures.
func (m Manifest) Validate() error {
	if _, ok := parseVersion(m.Version); !ok {
		return fmt.Errorf("manifest: version %q is not semver", m.Version)
	}
	if p := m.Rollout(); p < 0 || p > 100 {
		return fmt.Errorf("manifest: rollout_percent %d out of range 0-100", p)
	}
	for plat, a := range m.Platforms {
		u, err := url.Parse(a.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("manifest: %s: url %q must be https", plat, a.URL)
		}
		if !sha256Hex.MatchString(a.SHA256) {
			return fmt.Errorf("manifest: %s: sha256 must be 64 lowercase hex digits", plat)
		}
		if _, err := base64.StdEncoding.DecodeString(a.Signature); err != nil || a.Signature == "" {
			return fmt.Errorf("manifest: %s: signature must be a base64 minisign signature", plat)
		}
	}
	return nil
}

// ExpectedComment is the trusted comment a release archive's signature must
// carry. Signing it binds the signature to one version and platform, so an
// old signed archive can't be passed off as a newer release.
func ExpectedComment(version, platform string) string {
	return "sb " + version + " " + platform
}

// ManifestComment is the trusted comment a channel manifest's own signature
// (<channel>.json.minisig) must carry, e.g.
// "sb-manifest stable 2026-10-01T12:00:00Z". It binds the manifest to its
// channel and to its pub_date, which the updater remembers, so a beta
// manifest can't be served as stable and an older manifest can't be served
// again (see Updater.StateDir).
func ManifestComment(channel, pubDate string) string {
	return "sb-manifest " + channel + " " + pubDate
}

// VerifyManifest checks a channel manifest before any of it is trusted: sig,
// its .minisig file, must verify over exactly body with pk, and its trusted
// comment must be ManifestComment(channel, the manifest's pub_date). The
// manifest must then be valid, and the stable channel may not offer a
// pre-release. It returns the manifest and its pub_date.
func VerifyManifest(pk PublicKey, channel string, body, sig []byte) (Manifest, time.Time, error) {
	comment, err := pk.Verify(body, sig)
	if err != nil {
		return Manifest{}, time.Time{}, fmt.Errorf("the %s manifest: %w; not trusting it", channel, err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, time.Time{}, fmt.Errorf("read the %s manifest: %w", channel, err)
	}
	pub, err := time.Parse(time.RFC3339, m.PubDate)
	if err != nil {
		return Manifest{}, time.Time{}, fmt.Errorf("manifest: pub_date %q is not an RFC 3339 time", m.PubDate)
	}
	if want := ManifestComment(channel, m.PubDate); comment != want {
		return Manifest{}, time.Time{}, fmt.Errorf("%w: the %s manifest is signed as %q, not %q; not trusting it", ErrBadSignature, channel, comment, want)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, time.Time{}, err
	}
	if v, _ := parseVersion(m.Version); channel == "stable" && len(v.pre) > 0 {
		return Manifest{}, time.Time{}, fmt.Errorf("manifest: the stable channel offers pre-release %s; not installing it", m.Version)
	}
	return m, pub, nil
}

var errNoPlatform = errors.New("no build for this platform in the manifest")
