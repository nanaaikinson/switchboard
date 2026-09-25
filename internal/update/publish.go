package update

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PublishOptions describe one release for BuildManifest and BuildTrayManifest.
type PublishOptions struct {
	Version   string // vX.Y.Z[-pre]
	BaseURL   string // where the files are downloaded from, e.g. the GitHub release
	PublicKey string // the release key; every signature is checked against it
	Rollout   int    // rollout_percent
	Notes     string
	PubDate   string // RFC 3339
}

var archiveName = regexp.MustCompile(`^sb_([^_]+)_([a-z0-9]+)_([a-z0-9]+)\.(tar\.gz|zip)$`)

// BuildManifest makes the sb manifest from release archives, each with a
// minisign signature next to it (<archive>.minisig). It fails unless every
// signature verifies and names its exact version and platform, so a release
// can't publish a manifest that sb would refuse.
func BuildManifest(o PublishOptions, archives []string) (Manifest, error) {
	pk, err := ParsePublicKey(o.PublicKey)
	if err != nil {
		return Manifest{}, err
	}
	m := newManifest(o)
	for _, path := range archives {
		name := filepath.Base(path)
		parts := archiveName.FindStringSubmatch(name)
		if parts == nil || "v"+parts[1] != "v"+strings.TrimPrefix(o.Version, "v") {
			return Manifest{}, fmt.Errorf("%s is not an sb %s release archive", name, o.Version)
		}
		platform := parts[2] + "-" + parts[3]
		data, sig, err := readSigned(path, path+".minisig")
		if err != nil {
			return Manifest{}, err
		}
		comment, err := pk.Verify(data, sig)
		if err != nil {
			return Manifest{}, fmt.Errorf("%s: %w", name, err)
		}
		if want := ExpectedComment(o.Version, platform); comment != want {
			return Manifest{}, fmt.Errorf("%s: signed with trusted comment %q, want %q (sign with: minisign -S -t %q -m %s)", name, comment, want, want, name)
		}
		sum := sha256.Sum256(data)
		m.Platforms[platform] = Asset{
			URL: strings.TrimSuffix(o.BaseURL, "/") + "/" + name, SHA256: hex.EncodeToString(sum[:]),
			Signature: base64.StdEncoding.EncodeToString(sig),
		}
	}
	return m, m.Validate()
}

// TrayManifest is the Tauri updater's static JSON format, plus
// rollout_percent, which the tray app reads itself.
type TrayManifest struct {
	Version        string                  `json:"version"`
	Notes          string                  `json:"notes,omitempty"`
	PubDate        string                  `json:"pub_date,omitempty"`
	RolloutPercent int                     `json:"rollout_percent"`
	Platforms      map[string]TrayPlatform `json:"platforms"`
}

// TrayPlatform is one Tauri target's update bundle.
type TrayPlatform struct {
	Signature string `json:"signature"` // the .sig Tauri writes: a base64 minisign signature
	URL       string `json:"url"`
}

// BuildTrayManifest makes the tray app's manifest from Tauri's updater
// bundles (createUpdaterArtifacts), each with its .sig: Switchboard.app.tar.gz
// (universal, so both macOS targets) and the NSIS *-setup.exe for Windows.
func BuildTrayManifest(o PublishOptions, bundles []string) (TrayManifest, error) {
	pk, err := ParsePublicKey(o.PublicKey)
	if err != nil {
		return TrayManifest{}, err
	}
	m := TrayManifest{Version: strings.TrimPrefix(o.Version, "v"), Notes: o.Notes, PubDate: o.PubDate, RolloutPercent: o.Rollout, Platforms: map[string]TrayPlatform{}}
	for _, path := range bundles {
		name := filepath.Base(path)
		var targets []string
		switch {
		case strings.HasSuffix(name, ".app.tar.gz"):
			targets = []string{"darwin-aarch64", "darwin-x86_64"}
		case strings.HasSuffix(name, "-setup.exe"):
			targets = []string{"windows-x86_64"}
		default:
			return TrayManifest{}, fmt.Errorf("%s is not a tray updater bundle (.app.tar.gz or -setup.exe)", name)
		}
		data, sig64, err := readSigned(path, path+".sig")
		if err != nil {
			return TrayManifest{}, err
		}
		sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig64)))
		if err != nil {
			return TrayManifest{}, fmt.Errorf("%s.sig is not base64: %w", name, err)
		}
		if _, err := pk.Verify(data, sig); err != nil {
			return TrayManifest{}, fmt.Errorf("%s: %w", name, err)
		}
		for _, t := range targets {
			m.Platforms[t] = TrayPlatform{Signature: strings.TrimSpace(string(sig64)), URL: strings.TrimSuffix(o.BaseURL, "/") + "/" + name}
		}
	}
	if m.RolloutPercent < 0 || m.RolloutPercent > 100 {
		return TrayManifest{}, fmt.Errorf("rollout %d out of range 0-100", m.RolloutPercent)
	}
	return m, nil
}

func newManifest(o PublishOptions) Manifest {
	r := o.Rollout
	return Manifest{Version: o.Version, Notes: o.Notes, PubDate: o.PubDate, RolloutPercent: &r, Platforms: map[string]Asset{}}
}

func readSigned(path, sigPath string) ([]byte, []byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: release files named on the command line
	if err != nil {
		return nil, nil, err
	}
	sig, err := os.ReadFile(sigPath) //nolint:gosec // G304: as above
	if err != nil {
		return nil, nil, fmt.Errorf("%s has no signature: %w", filepath.Base(path), err)
	}
	return data, sig, nil
}
