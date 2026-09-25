package update

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
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

var errNoPlatform = errors.New("no build for this platform in the manifest")
