//go:build unix

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"

	"github.com/nanaaikinson/switchboard/internal/platform"
	"github.com/nanaaikinson/switchboard/internal/update"
)

// restartSpy records daemon restarts instead of touching the real service.
type restartSpy struct {
	platform.Platform
	restarts int
}

func (s *restartSpy) RestartDaemon() (bool, error) { s.restarts++; return true, nil }

type signer struct {
	id   [8]byte
	priv ed25519.PrivateKey
	pub  string
}

func newSigner(t *testing.T) signer {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s := signer{priv: priv}
	_, _ = rand.Read(s.id[:])
	s.pub = base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), s.id[:]...), pub...))
	return s
}

// sign returns a base64 minisign signature file, as manifests carry it.
func (s signer) sign(msg []byte, comment string) string {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(s.priv, h[:])
	global := ed25519.Sign(s.priv, append(append([]byte(nil), sig...), comment...))
	file := fmt.Sprintf("untrusted comment: test\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(append(append([]byte("ED"), s.id[:]...), sig...)), comment,
		base64.StdEncoding.EncodeToString(global))
	return base64.StdEncoding.EncodeToString([]byte(file))
}

// script is a stand-in sb that only answers --version.
func script(v string) []byte { return []byte("#!/bin/sh\necho 'sb version " + v + "'\n") }

type updateEnv struct {
	exe      string
	spy      *restartSpy
	manifest update.Manifest
	archive  []byte
}

// newUpdateEnv serves version v, signed by key, and installs a fake sb at
// v1.0.0 in a temp dir as the binary to update.
func newUpdateEnv(t *testing.T, key signer, v string, binary []byte, rollout *int) *updateEnv {
	t.Helper()
	configDir(t)
	e := &updateEnv{exe: filepath.Join(t.TempDir(), "sb"), spy: &restartSpy{}}
	if err := os.WriteFile(e.exe, script("v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "sb_x/sb", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(binary)
	_ = tw.Close()
	_ = gz.Close()
	e.archive = buf.Bytes()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stable.json":
			_ = json.NewEncoder(w).Encode(e.manifest)
		case "/sb.tar.gz":
			_, _ = w.Write(e.archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	plat := runtime.GOOS + "-" + runtime.GOARCH
	sum := sha256.Sum256(e.archive)
	e.manifest = update.Manifest{Version: v, RolloutPercent: rollout, Platforms: map[string]update.Asset{plat: {
		URL: srv.URL + "/sb.tar.gz", SHA256: hex.EncodeToString(sum[:]), Signature: key.sign(e.archive, "sb "+v+" "+plat),
	}}}

	origKey, origHTTP, origSelf, origVersion, origPlatform := update.ReleaseKey, updateHTTP, selfPath, version, currentPlatform
	update.ReleaseKey, updateHTTP, version = key.pub, srv.Client(), "v1.0.0"
	selfPath = func() (string, error) { return e.exe, nil }
	currentPlatform = func() (platform.Platform, platform.Options) { return e.spy, platform.Options{} }
	t.Setenv("SB_UPDATE_URL", srv.URL)
	t.Cleanup(func() {
		update.ReleaseKey, updateHTTP, selfPath, version, currentPlatform = origKey, origHTTP, origSelf, origVersion, origPlatform
	})
	return e
}

func (e *updateEnv) current(t *testing.T) string {
	t.Helper()
	v, err := runVersion(t.Context(), e.exe)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (e *updateEnv) leftovers(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(e.exe), ".sb-new-*"))
	return m
}

func TestSelfUpdateAndRollback(t *testing.T) {
	key := newSigner(t)
	e := newUpdateEnv(t, key, "v1.1.0", script("v1.1.0"), nil)

	out := mustRun(t, "self-update", "--check")
	if !strings.Contains(out, "latest on stable: v1.1.0") || !strings.Contains(out, "An update is available") || e.current(t) != "v1.0.0" {
		t.Fatalf("--check changed something or said:\n%s", out)
	}
	out = mustRun(t, "self-update")
	for _, want := range []string{"Updated " + e.exe + " from v1.0.0 to v1.1.0", "sb.old", "sb rollback", "Restarted the daemon."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if e.current(t) != "v1.1.0" || e.spy.restarts != 1 {
		t.Errorf("after update: %s, %d restarts", e.current(t), e.spy.restarts)
	}
	if v, _ := runVersion(t.Context(), update.OldPath(e.exe)); v != "v1.0.0" {
		t.Errorf("sb.old is %q", v)
	}

	version = "v1.1.0"
	if out := mustRun(t, "self-update"); !strings.Contains(out, "Already up to date.") {
		t.Errorf("second run:\n%s", out)
	}

	out = mustRun(t, "rollback")
	if !strings.Contains(out, "Rolled back "+e.exe+" to v1.0.0") || e.current(t) != "v1.0.0" || e.spy.restarts != 2 {
		t.Errorf("rollback: %s\n%s", e.current(t), out)
	}
	mustRun(t, "rollback")
	if e.current(t) != "v1.1.0" {
		t.Errorf("rolling back twice should go forward: %s", e.current(t))
	}
}

func TestSelfUpdateRefuses(t *testing.T) {
	zero := 0
	for name, tc := range map[string]struct {
		setup  func(t *testing.T) *updateEnv
		errMsg string // "" means no error, but no change either
		out    string
	}{
		"not in the rollout yet": {func(t *testing.T) *updateEnv {
			return newUpdateEnv(t, newSigner(t), "v1.1.0", script("v1.1.0"), &zero)
		}, "", "rolling out to 0% of installs"},
		"signed with another key": {func(t *testing.T) *updateEnv {
			e := newUpdateEnv(t, newSigner(t), "v1.1.0", script("v1.1.0"), nil)
			update.ReleaseKey = newSigner(t).pub
			return e
		}, "not the update key", ""},
		"tampered archive": {func(t *testing.T) *updateEnv {
			e := newUpdateEnv(t, newSigner(t), "v1.1.0", script("v1.1.0"), nil)
			e.archive = append(e.archive[:len(e.archive):len(e.archive)], 0)
			return e
		}, "SHA-256", ""},
		"binary reports another version": {func(t *testing.T) *updateEnv {
			return newUpdateEnv(t, newSigner(t), "v1.1.0", script("v0.0.1"), nil)
		}, "doesn't work (it reports version v0.0.1, not v1.1.0)", ""},
		"binary doesn't run": {func(t *testing.T) *updateEnv {
			return newUpdateEnv(t, newSigner(t), "v1.1.0", []byte("not a program"), nil)
		}, "doesn't work", ""},
		"no release key in this build": {func(t *testing.T) *updateEnv {
			e := newUpdateEnv(t, newSigner(t), "v1.1.0", script("v1.1.0"), nil)
			update.ReleaseKey = ""
			return e
		}, "no update signing key", ""},
		"installed by Homebrew": {func(t *testing.T) *updateEnv {
			e := newUpdateEnv(t, newSigner(t), "v1.1.0", script("v1.1.0"), nil)
			selfPath = func() (string, error) { return "/opt/homebrew/Cellar/switchboard/1.0.0/bin/sb", nil }
			return e
		}, "installed by Homebrew, which keeps it up to date; instead run: brew upgrade sb", ""},
		"SB_UPDATE_URL over plain http": {func(t *testing.T) *updateEnv {
			e := newUpdateEnv(t, newSigner(t), "v1.1.0", script("v1.1.0"), nil)
			t.Setenv("SB_UPDATE_URL", "http://updates.example.com")
			return e
		}, "SB_UPDATE_URL: http://updates.example.com is not https", ""},
		"development build": {func(t *testing.T) *updateEnv {
			e := newUpdateEnv(t, newSigner(t), "v1.1.0", script("v1.1.0"), nil)
			version = "dev"
			return e
		}, "development build", ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := tc.setup(t)
			out, err := run(t, "self-update")
			if tc.errMsg == "" && err != nil || tc.errMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.errMsg)) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.errMsg, out)
			}
			if !strings.Contains(out, tc.out) {
				t.Errorf("output missing %q:\n%s", tc.out, out)
			}
			if e.current(t) != "v1.0.0" || e.spy.restarts != 0 {
				t.Errorf("sb changed to %s (%d restarts)", e.current(t), e.spy.restarts)
			}
			if _, err := os.Stat(update.OldPath(e.exe)); err == nil {
				t.Error("sb.old was created")
			}
			if l := e.leftovers(t); len(l) != 0 {
				t.Errorf("staged files left behind: %v", l)
			}
		})
	}
}

func TestSelfUpdateBadChannelAndNoRollback(t *testing.T) {
	newUpdateEnv(t, newSigner(t), "v1.1.0", script("v1.1.0"), nil)
	if _, err := run(t, "self-update", "--channel", "nightly"); err == nil || !strings.Contains(err.Error(), "stable or beta") {
		t.Errorf("bad channel: %v", err)
	}
	if _, err := run(t, "rollback"); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Errorf("rollback without sb.old: %v", err)
	}
}
