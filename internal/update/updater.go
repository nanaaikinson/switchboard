package update

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL serves the manifests; override it with SB_UPDATE_URL.
const DefaultBaseURL = "https://nanaaikinson.github.io/switchboard-updates"

// Channels a manifest can be fetched for.
var Channels = []string{"stable", "beta"}

// ErrNoKey means this build has no update public key, so nothing can be
// verified and nothing is installed.
var ErrNoKey = errors.New("this build of sb has no update signing key")

const (
	maxManifest = 1 << 20
	maxArchive  = 300 << 20
)

// Updater checks a channel and downloads verified releases.
type Updater struct {
	BaseURL   string       // without the trailing /<channel>.json
	PublicKey string       // minisign public key; empty means no updates
	Current   string       // this binary's version
	Platform  string       // GOOS-GOARCH
	InstallID string       // for the rollout bucket
	HTTP      *http.Client // nil is a client with timeouts
}

// Check is what a channel offers this install.
type Check struct {
	Manifest  Manifest
	Asset     Asset
	Newer     bool // the release is newer than Current
	Bucket    int  // this install's bucket for the release, 0-99
	InRollout bool // Bucket < the release's rollout_percent
}

func (u Updater) client() *http.Client {
	if u.HTTP != nil {
		return u.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// Check fetches the channel's manifest and compares it with this install.
func (u Updater) Check(ctx context.Context, channel string) (Check, error) {
	if u.PublicKey == "" {
		return Check{}, ErrNoKey
	}
	cur, ok := parseVersion(u.Current)
	if !ok {
		return Check{}, fmt.Errorf("this is a development build (version %q); self-update needs a release build", u.Current)
	}
	url := strings.TrimSuffix(u.BaseURL, "/") + "/" + channel + ".json"
	body, err := u.get(ctx, url, maxManifest)
	if err != nil {
		return Check{}, fmt.Errorf("fetch the %s manifest: %w", channel, err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Check{}, fmt.Errorf("read %s: %w", url, err)
	}
	if err := m.Validate(); err != nil {
		return Check{}, err
	}
	asset, ok := m.Platforms[u.Platform]
	if !ok {
		return Check{}, fmt.Errorf("%w (%s)", errNoPlatform, u.Platform)
	}
	next, _ := parseVersion(m.Version)
	b := Bucket(u.InstallID, m.Version)
	return Check{Manifest: m, Asset: asset, Newer: compareVersions(next, cur) > 0, Bucket: b, InRollout: InRollout(b, m.Rollout())}, nil
}

// Download fetches c's archive and verifies, in order, its SHA-256, its
// minisign signature with the embedded key, and that the signature's trusted
// comment names this exact version and platform. Only then is sb extracted.
func (u Updater) Download(ctx context.Context, c Check) ([]byte, error) {
	pk, err := ParsePublicKey(u.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("embedded update key: %w", err)
	}
	archive, err := u.get(ctx, c.Asset.URL, maxArchive)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", c.Asset.URL, err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != c.Asset.SHA256 {
		return nil, fmt.Errorf("the download's SHA-256 is %s, the manifest says %s; not installing it", got, c.Asset.SHA256)
	}
	sig, err := base64.StdEncoding.DecodeString(c.Asset.Signature)
	if err != nil {
		return nil, fmt.Errorf("%w: signature isn't base64", ErrBadSignature)
	}
	comment, err := pk.Verify(archive, sig)
	if err != nil {
		return nil, fmt.Errorf("%w; not installing it", err)
	}
	if want := ExpectedComment(c.Manifest.Version, u.Platform); comment != want {
		return nil, fmt.Errorf("%w: the signature is for %q, not %q; not installing it", ErrBadSignature, comment, want)
	}
	return Extract(archive, strings.HasPrefix(u.Platform, "windows-"))
}

func (u Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sb/"+u.Current)
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", url, limit)
	}
	return b, nil
}
