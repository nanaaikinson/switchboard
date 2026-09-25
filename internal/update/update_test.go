package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// testKey is a minisign key pair made for one test.
type testKey struct {
	id   [8]byte
	priv ed25519.PrivateKey
	pub  string // the base64 public key line
}

func newKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var k testKey
	_, _ = rand.Read(k.id[:])
	k.priv = priv
	k.pub = base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), k.id[:]...), pub...))
	return k
}

// sign makes a minisign signature file, pre-hashed ("ED") unless legacy.
func (k testKey) sign(msg []byte, comment string, legacy bool) []byte {
	alg, signed := "ED", msg
	if legacy {
		alg = "Ed"
	} else {
		h := blake2b.Sum512(msg)
		signed = h[:]
	}
	sig := ed25519.Sign(k.priv, signed)
	global := ed25519.Sign(k.priv, append(append([]byte(nil), sig...), comment...))
	line := base64.StdEncoding.EncodeToString(append(append([]byte(alg), k.id[:]...), sig...))
	return []byte(fmt.Sprintf("untrusted comment: test\n%s\ntrusted comment: %s\n%s\n", line, comment, base64.StdEncoding.EncodeToString(global)))
}

func TestVerifyTauriSignerFixture(t *testing.T) {
	// A real pre-hashed signature from `tauri signer sign`, with a throwaway
	// key whose secret half was deleted.
	pubFile, _ := os.ReadFile("testdata/tauri-signer.pub")
	msg, _ := os.ReadFile("testdata/tauri-signer-message.txt")
	sig, _ := os.ReadFile("testdata/tauri-signer-message.txt.minisig")
	pk, err := ParsePublicKey(string(pubFile))
	if err != nil {
		t.Fatal(err)
	}
	if pk.KeyID() != "F99E25F2AB9ABB02" {
		t.Errorf("key ID %s, want the one minisign prints", pk.KeyID())
	}
	comment, err := pk.Verify(msg, sig)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !strings.Contains(comment, "file:message.txt") {
		t.Errorf("trusted comment %q", comment)
	}
	if _, err := pk.Verify(append(msg, '!'), sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("changed message: %v", err)
	}
}

func TestVerify(t *testing.T) {
	k, other := newKey(t), newKey(t)
	pk, err := ParsePublicKey("untrusted comment: minisign public key\n" + k.pub + "\n")
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("the archive")
	for _, legacy := range []bool{false, true} {
		if c, err := pk.Verify(msg, k.sign(msg, "sb v1.2.3 darwin-arm64", legacy)); err != nil || c != "sb v1.2.3 darwin-arm64" {
			t.Errorf("legacy=%v: %q, %v", legacy, c, err)
		}
	}

	good := k.sign(msg, "sb v1.2.3 darwin-arm64", false)
	lines := strings.Split(string(good), "\n")
	tamperedComment := strings.Join([]string{lines[0], lines[1], "trusted comment: sb v9.9.9 darwin-arm64", lines[3]}, "\n")
	// Same key ID, different key: a forged signature.
	forged := other
	forged.id = k.id
	for name, tc := range map[string]struct {
		msg []byte
		sig []byte
		why string
	}{
		"changed file":             {[]byte("the archivE"), good, "doesn't match"},
		"other key":                {msg, other.sign(msg, "c", false), "not the update key"},
		"forged with same key ID":  {msg, forged.sign(msg, "c", false), "doesn't match"},
		"changed trusted comment":  {msg, []byte(tamperedComment), "trusted comment was changed"},
		"empty":                    {msg, nil, "malformed"},
		"garbage":                  {msg, []byte("untrusted comment: x\n!!!\ntrusted comment: y\n!!!\n"), "malformed"},
		"unknown algorithm":        {msg, []byte(strings.Replace(string(good), lines[1], base64.StdEncoding.EncodeToString(append([]byte("XX"), mustB64(lines[1])[2:]...)), 1)), "unknown signature algorithm"},
		"truncated signature line": {msg, []byte(strings.Replace(string(good), lines[1], lines[1][:20], 1)), "malformed"},
	} {
		_, err := pk.Verify(tc.msg, tc.sig)
		if !errors.Is(err, ErrBadSignature) || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%s: err = %v, want ErrBadSignature (%s)", name, err, tc.why)
		}
	}
	for _, bad := range []string{"", "RWQ", base64.StdEncoding.EncodeToString([]byte("Ed1234567890")), "untrusted comment: x\nnot base64"} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("ParsePublicKey(%q) accepted", bad)
		}
	}
}

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestBucket(t *testing.T) {
	if Bucket("abc", "v1.0.0") != Bucket("abc", "1.0.0") {
		t.Error("bucket depends on the v prefix")
	}
	// Pinned values: changing the algorithm would reshuffle every rollout.
	if got := Bucket("00000000000000000000000000000001", "1.0.0"); got != 9 {
		t.Errorf("bucket = %d, want 9", got)
	}
	if got := Bucket("00000000000000000000000000000001", "2.0.0"); got != 12 {
		t.Errorf("bucket = %d, want 12", got)
	}
	// Roughly uniform over many installs, and reshuffled per version.
	var counts [10]int
	same := 0
	for i := range 10000 {
		id := fmt.Sprintf("%032x", i)
		b := Bucket(id, "1.0.0")
		if b < 0 || b > 99 {
			t.Fatalf("bucket %d out of range", b)
		}
		counts[b/10]++
		if Bucket(id, "1.1.0") == b {
			same++
		}
	}
	for d, n := range counts {
		if n < 850 || n > 1150 {
			t.Errorf("bucket decile %d has %d of 10000 installs", d, n)
		}
	}
	if same > 300 { // ~100 expected by chance
		t.Errorf("%d installs kept their bucket across versions", same)
	}
}

func TestInRollout(t *testing.T) {
	for _, tc := range []struct {
		bucket, percent int
		want            bool
	}{
		{0, 0, false}, {99, 0, false}, // 0%: nobody
		{0, 1, true}, {1, 1, false}, // 1%: bucket 0 only
		{24, 25, true}, {25, 25, false},
		{0, 100, true}, {99, 100, true}, // 100%: everyone
	} {
		if got := InRollout(tc.bucket, tc.percent); got != tc.want {
			t.Errorf("InRollout(%d, %d) = %v", tc.bucket, tc.percent, got)
		}
	}
	// The share of installs updated tracks the percentage.
	in := 0
	for i := range 10000 {
		if InRollout(Bucket(fmt.Sprintf("id-%d", i), "2.0.0"), 30) {
			in++
		}
	}
	if in < 2700 || in > 3300 {
		t.Errorf("30%% rollout reached %d of 10000", in)
	}
}

func TestInstallID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	id, err := InstallID(dir)
	if err != nil || len(id) != 32 {
		t.Fatalf("InstallID = %q, %v", id, err)
	}
	again, _ := InstallID(dir)
	if again != id {
		t.Error("install ID changed between calls")
	}
	fi, _ := os.Stat(filepath.Join(dir, InstallIDFile))
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if err := os.WriteFile(filepath.Join(dir, InstallIDFile), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := InstallID(dir); fresh == id || len(fresh) != 32 {
		t.Errorf("corrupt ID not replaced: %q", fresh)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "v1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.2", "1.0.0-rc.1", 1},
		{"1.0.0-rc.10", "1.0.0-rc.9", 1},
		{"1.0.0-beta", "1.0.0-alpha", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1},
		{"1.0.0-alpha.beta", "1.0.0-alpha.1", 1}, // text beats numbers
		{"1.0.0+build.9", "1.0.0", 0},
	} {
		a, ok1 := parseVersion(tc.a)
		b, ok2 := parseVersion(tc.b)
		if !ok1 || !ok2 {
			t.Fatalf("parse %q %q", tc.a, tc.b)
		}
		if got := compareVersions(a, b); got != tc.want {
			t.Errorf("compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := compareVersions(b, a); got != -tc.want {
			t.Errorf("compare(%s, %s) = %d, want %d", tc.b, tc.a, got, -tc.want)
		}
	}
	for _, bad := range []string{"dev", "1.2", "1.2.3.4", "01.2.3", "1.2.x", "1.2.3-", ""} {
		if _, ok := parseVersion(bad); ok {
			t.Errorf("parseVersion(%q) accepted", bad)
		}
	}
}

func TestDetectManaged(t *testing.T) {
	dpkg := func(p string) ([]byte, error) {
		if p == "/var/lib/dpkg/info/switchboard.list" {
			return []byte("/.\n/usr\n/usr/bin\n/usr/bin/sb\n"), nil
		}
		return nil, os.ErrNotExist
	}
	none := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	rpmOwns := func(name string, args ...string) error {
		if name == "rpm" && args[len(args)-1] == "/usr/bin/sb" {
			return nil
		}
		return errors.New("not owned")
	}
	for _, tc := range []struct {
		exe  string
		sys  System
		want string
	}{
		{"/opt/homebrew/Cellar/switchboard/0.2.0/bin/sb", System{ReadFile: none}, "Homebrew"},
		{"/usr/local/Cellar/switchboard/0.2.0/bin/sb", System{ReadFile: none}, "Homebrew"},
		{"/home/linuxbrew/.linuxbrew/bin/sb", System{ReadFile: none}, "Homebrew"},
		{`C:\Users\me\AppData\Local\Microsoft\WinGet\Packages\Switchboard_x\sb.exe`, System{ReadFile: none}, "winget"},
		{`C:\Users\me\scoop\apps\switchboard\current\sb.exe`, System{ReadFile: none}, "Scoop"},
		{"/Applications/Switchboard.app/Contents/MacOS/sb", System{ReadFile: none}, "the Switchboard app"},
		{"/usr/bin/sb", System{ReadFile: dpkg}, "a Linux package (.deb)"},
		{"/usr/bin/sb", System{ReadFile: none, Run: rpmOwns}, "a Linux package (.rpm)"},
		{"/home/me/.local/bin/sb", System{ReadFile: dpkg, Run: rpmOwns}, ""}, // install.sh
		{"/usr/local/bin/sb", System{ReadFile: none, Run: rpmOwns}, ""},      // install.sh --global
	} {
		got := DetectManaged(tc.exe, tc.sys)
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("%s: managed by %s, want standalone", tc.exe, got.By)
		case tc.want != "" && (got == nil || got.By != tc.want):
			t.Errorf("%s: %+v, want %s", tc.exe, got, tc.want)
		case got != nil && got.Update == "":
			t.Errorf("%s: no update instructions", tc.exe)
		}
	}
}

func TestSwapAndRollback(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sb")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	if err := Rollback(exe); !errors.Is(err, ErrNoOld) {
		t.Errorf("rollback with nothing to roll back to: %v", err)
	}
	staged, err := Stage(exe, []byte("v2"))
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(staged); runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 || filepath.Dir(staged) != dir {
		t.Errorf("staged %s mode %v", staged, fi.Mode())
	}
	if err := Swap(exe, staged); err != nil {
		t.Fatal(err)
	}
	if read(exe) != "v2" || read(filepath.Join(dir, "sb.old")) != "v1" {
		t.Fatalf("after swap: sb=%q sb.old=%q", read(exe), read(filepath.Join(dir, "sb.old")))
	}
	if err := Rollback(exe); err != nil {
		t.Fatal(err)
	}
	if read(exe) != "v1" || read(filepath.Join(dir, "sb.old")) != "v2" {
		t.Errorf("after rollback: sb=%q sb.old=%q", read(exe), read(filepath.Join(dir, "sb.old")))
	}
	if err := Rollback(exe); err != nil || read(exe) != "v2" {
		t.Errorf("rolling back again should roll forward: %v, sb=%q", err, read(exe))
	}
	// A failed install puts the old binary back.
	if err := Swap(exe, filepath.Join(dir, "missing")); err == nil || read(exe) != "v2" {
		t.Errorf("failed swap: %v, sb=%q", err, read(exe))
	}
	if OldPath(`C:\sb\sb.exe`) != `C:\sb\sb.old.exe` {
		t.Errorf("windows old path %s", OldPath(`C:\sb\sb.exe`))
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestExtract(t *testing.T) {
	b, err := Extract(tarGz(t, map[string]string{"sb_1.0.0_linux_amd64/README": "x", "sb_1.0.0_linux_amd64/sb": "binary"}), false)
	if err != nil || string(b) != "binary" {
		t.Errorf("tar.gz: %q, %v", b, err)
	}
	if _, err := Extract(tarGz(t, map[string]string{"x/sbx": "no"}), false); err == nil {
		t.Error("archive without sb accepted")
	}
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("sb_1.0.0_windows_amd64/sb.exe")
	_, _ = w.Write([]byte("exe"))
	_ = zw.Close()
	if b, err := Extract(zbuf.Bytes(), true); err != nil || string(b) != "exe" {
		t.Errorf("zip: %q, %v", b, err)
	}
	if _, err := Extract([]byte("not an archive"), false); err == nil {
		t.Error("garbage accepted")
	}
}

// release serves a manifest and an archive for the Updater tests.
type release struct {
	manifest Manifest
	archive  []byte
	srv      *httptest.Server
}

func newRelease(t *testing.T, k testKey, version, platform, comment string, rollout *int) *release {
	t.Helper()
	r := &release{archive: tarGz(t, map[string]string{"sb_x/sb": "new sb " + version})}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/stable.json":
			_ = json.NewEncoder(w).Encode(r.manifest)
		case "/sb.tar.gz":
			_, _ = w.Write(r.archive)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	sum := sha256.Sum256(r.archive)
	r.manifest = Manifest{Version: version, RolloutPercent: rollout, Platforms: map[string]Asset{platform: {
		URL: r.srv.URL + "/sb.tar.gz", SHA256: hex.EncodeToString(sum[:]),
		Signature: base64.StdEncoding.EncodeToString(k.sign(r.archive, comment, false)),
	}}}
	return r
}

func (r *release) updater(k testKey, current string) Updater {
	return Updater{BaseURL: r.srv.URL, PublicKey: k.pub, Current: current, Platform: "darwin-arm64",
		InstallID: "00000000000000000000000000000001", HTTP: r.srv.Client()}
}

func TestUpdaterHappyPath(t *testing.T) {
	k := newKey(t)
	r := newRelease(t, k, "v1.3.0", "darwin-arm64", "sb v1.3.0 darwin-arm64", nil)
	u := r.updater(k, "v1.2.0")
	c, err := u.Check(context.Background(), "stable")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Newer || !c.InRollout || c.Manifest.Rollout() != 100 {
		t.Errorf("check %+v", c)
	}
	bin, err := u.Download(context.Background(), c)
	if err != nil || string(bin) != "new sb v1.3.0" {
		t.Errorf("download: %q, %v", bin, err)
	}
	u.Current = "v1.3.0"
	if c, _ := u.Check(context.Background(), "stable"); c.Newer {
		t.Error("same version reported as newer")
	}
}

func TestUpdaterRollout(t *testing.T) {
	k := newKey(t)
	id := "00000000000000000000000000000001"
	b := Bucket(id, "v1.3.0")
	below, at := b, b+1 // rollout_percent == bucket excludes it; bucket+1 includes it
	r := newRelease(t, k, "v1.3.0", "darwin-arm64", "sb v1.3.0 darwin-arm64", &below)
	c, err := r.updater(k, "v1.2.0").Check(context.Background(), "stable")
	if err != nil || c.InRollout || c.Bucket != b {
		t.Errorf("rollout %d%%, bucket %d: %+v %v", below, b, c, err)
	}
	r.manifest.RolloutPercent = &at
	if c, _ := r.updater(k, "v1.2.0").Check(context.Background(), "stable"); !c.InRollout {
		t.Errorf("rollout %d%%, bucket %d: not included", at, b)
	}
}

func TestUpdaterRefusesBadDownloads(t *testing.T) {
	k, attacker := newKey(t), newKey(t)
	for name, tc := range map[string]struct {
		edit func(r *release)
		why  string
	}{
		"archive swapped, hash updated, signature kept": {func(r *release) {
			r.archive = tarGz(t, map[string]string{"sb_x/sb": "malware"})
			sum := sha256.Sum256(r.archive)
			a := r.manifest.Platforms["darwin-arm64"]
			a.SHA256 = hex.EncodeToString(sum[:])
			r.manifest.Platforms["darwin-arm64"] = a
		}, "doesn't match its signature"},
		"archive swapped, hash not updated": {func(r *release) {
			r.archive = tarGz(t, map[string]string{"sb_x/sb": "malware"})
		}, "SHA-256"},
		"signed by another key": {func(r *release) {
			a := r.manifest.Platforms["darwin-arm64"]
			a.Signature = base64.StdEncoding.EncodeToString(attacker.sign(r.archive, "sb v1.3.0 darwin-arm64", false))
			r.manifest.Platforms["darwin-arm64"] = a
		}, "not the update key"},
		"old release replayed as newer": {func(r *release) {
			a := r.manifest.Platforms["darwin-arm64"]
			a.Signature = base64.StdEncoding.EncodeToString(k.sign(r.archive, "sb v1.1.0 darwin-arm64", false))
			r.manifest.Platforms["darwin-arm64"] = a
		}, `the signature is for "sb v1.1.0 darwin-arm64"`},
		"another platform's build": {func(r *release) {
			a := r.manifest.Platforms["darwin-arm64"]
			a.Signature = base64.StdEncoding.EncodeToString(k.sign(r.archive, "sb v1.3.0 linux-amd64", false))
			r.manifest.Platforms["darwin-arm64"] = a
		}, "linux-amd64"},
		"signature isn't base64": {func(r *release) {
			a := r.manifest.Platforms["darwin-arm64"]
			a.Signature = "@@@"
			r.manifest.Platforms["darwin-arm64"] = a
		}, "signature"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t, k, "v1.3.0", "darwin-arm64", "sb v1.3.0 darwin-arm64", nil)
			tc.edit(r)
			u := r.updater(k, "v1.2.0")
			c, err := u.Check(context.Background(), "stable")
			if err == nil {
				_, err = u.Download(context.Background(), c)
			}
			if err == nil || !strings.Contains(err.Error(), tc.why) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.why)
			}
		})
	}
}

func TestUpdaterCheckErrors(t *testing.T) {
	k := newKey(t)
	r := newRelease(t, k, "v1.3.0", "linux-amd64", "sb v1.3.0 linux-amd64", nil)
	u := r.updater(k, "v1.2.0")
	if _, err := u.Check(context.Background(), "stable"); !errors.Is(err, errNoPlatform) {
		t.Errorf("missing platform: %v", err)
	}
	if _, err := u.Check(context.Background(), "beta"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing channel: %v", err)
	}
	noKey := u
	noKey.PublicKey = ""
	if _, err := noKey.Check(context.Background(), "stable"); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
	dev := u
	dev.Current = "dev"
	if _, err := dev.Check(context.Background(), "stable"); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Errorf("dev build: %v", err)
	}
	bad := 150
	r.manifest.RolloutPercent = &bad
	if _, err := u.Check(context.Background(), "stable"); err == nil || !strings.Contains(err.Error(), "rollout_percent") {
		t.Errorf("bad rollout: %v", err)
	}
	r.manifest.RolloutPercent = nil
	a := r.manifest.Platforms["linux-amd64"]
	a.URL = "http://example.com/sb.tar.gz"
	r.manifest.Platforms["linux-amd64"] = a
	if _, err := u.Check(context.Background(), "stable"); err == nil || !strings.Contains(err.Error(), "must be https") {
		t.Errorf("http asset: %v", err)
	}
}
