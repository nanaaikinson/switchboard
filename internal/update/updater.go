package update

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	// StateDir keeps StateFile, the newest pub_date accepted per channel, so
	// an older manifest is refused. Empty turns that check off (tests only).
	StateDir string
	Now      func() time.Time // nil is time.Now
}

// Check is what a channel offers this install.
type Check struct {
	Manifest  Manifest
	Asset     Asset
	Newer     bool // the release is newer than Current
	Bucket    int  // this install's bucket for the release, 0-99
	InRollout bool // Bucket < the release's rollout_percent
}

// client is u.HTTP, or a client with timeouts, made to follow only the
// redirects CheckURL allows, so an https URL can't be redirected to http.
func (u Updater) client() *http.Client {
	c := http.Client{Timeout: 5 * time.Minute}
	if u.HTTP != nil {
		c = *u.HTTP
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return checkURL(req.URL)
	}
	return &c
}

// CheckURL reports whether updates may be fetched from raw: it must be https,
// or http to a loopback host (localhost, 127.0.0.1, ::1) for a local test
// server. Every request and redirect is checked the same way.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a URL", raw)
	}
	return checkURL(u)
}

func checkURL(u *url.URL) error {
	switch {
	case u.Host == "":
		return fmt.Errorf("%q is not an absolute URL", u.Redacted())
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("%s is not https; updates are only fetched over https (plain http only from localhost)", u.Redacted())
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Check fetches the channel's manifest and its signature, verifies it with
// VerifyManifest, checks its pub_date is neither in the future nor older than
// the newest one this install has accepted, and compares it with this
// install.
func (u Updater) Check(ctx context.Context, channel string) (Check, error) {
	if u.PublicKey == "" {
		return Check{}, ErrNoKey
	}
	pk, err := ParsePublicKey(u.PublicKey)
	if err != nil {
		return Check{}, fmt.Errorf("embedded update key: %w", err)
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
	sig, err := u.get(ctx, url+".minisig", maxManifest)
	if err != nil {
		return Check{}, fmt.Errorf("fetch the %s manifest's signature: %w", channel, err)
	}
	m, pub, err := VerifyManifest(pk, channel, body, sig)
	if err != nil {
		return Check{}, err
	}
	if err := u.checkFresh(channel, pub); err != nil {
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

// checkFresh refuses a manifest dated in the future, or older than the
// newest one accepted on channel, and otherwise remembers its date.
func (u Updater) checkFresh(channel string, pub time.Time) error {
	now := time.Now
	if u.Now != nil {
		now = u.Now
	}
	if pub.After(now().Add(MaxClockSkew)) {
		return fmt.Errorf("the %s manifest is dated %s, in the future; check this computer's clock, then try again", channel, pub.Format(time.RFC3339))
	}
	if u.StateDir == "" {
		return nil
	}
	last, err := lastPubDate(u.StateDir, channel)
	if err != nil {
		return err
	}
	if pub.Before(last) {
		return fmt.Errorf("the %s manifest is dated %s, older than one this install already accepted (%s); an old manifest can hold back updates, so it's refused. Try again later",
			channel, pub.Format(time.RFC3339), last.Format(time.RFC3339))
	}
	if pub.After(last) {
		return recordPubDate(u.StateDir, channel, pub)
	}
	return nil
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
	if err := CheckURL(url); err != nil {
		return nil, err
	}
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
