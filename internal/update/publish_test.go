package update

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSigned(t *testing.T, dir, name string, data, sig []byte, sigExt string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if sig != nil {
		if err := os.WriteFile(path+sigExt, sig, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestBuildManifestRoundTrip(t *testing.T) {
	k := newKey(t)
	dir := t.TempDir()
	archive := tarGz(t, map[string]string{"sb_1.4.0_darwin_arm64/sb": "sb 1.4.0"})
	path := writeSigned(t, dir, "sb_1.4.0_darwin_arm64.tar.gz", archive, k.sign(archive, "sb v1.4.0 darwin-arm64", false), ".minisig")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join(dir, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	pubDate := "2026-01-01T12:00:00Z"
	m, err := BuildManifest(PublishOptions{Version: "v1.4.0", BaseURL: srv.URL, PublicKey: k.pub, Rollout: 20, PubDate: pubDate}, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	a := m.Platforms["darwin-arm64"]
	if m.Rollout() != 20 || a.URL != srv.URL+"/sb_1.4.0_darwin_arm64.tar.gz" || len(a.SHA256) != 64 || m.PubDate != pubDate {
		t.Fatalf("manifest %+v", m)
	}
	// Published as the release workflow does: stable.json, signed for stable.
	body, _ := json.MarshalIndent(m, "", "  ")
	manifest := writeSigned(t, dir, "stable.json", body, k.sign(body, ManifestComment("stable", pubDate), false), ".minisig")
	if _, err := CheckSignedManifest(k.pub, "stable", manifest); err != nil {
		t.Fatalf("sb-manifest -verify: %v", err)
	}
	if _, err := CheckSignedManifest(k.pub, "beta", manifest); err == nil {
		t.Error("the stable manifest verified as beta")
	}
	// What the release publishes is exactly what the client accepts.
	u := Updater{BaseURL: srv.URL, PublicKey: k.pub, Current: "v1.3.0", Platform: "darwin-arm64", HTTP: srv.Client(), StateDir: t.TempDir()}
	c, err := u.Check(context.Background(), "stable")
	if err != nil || !c.Newer {
		t.Fatalf("client rejected the published manifest: %+v, %v", c, err)
	}
	bin, err := u.Download(context.Background(), c)
	if err != nil || string(bin) != "sb 1.4.0" {
		t.Errorf("client rejected the published archive: %q, %v", bin, err)
	}
}

func TestBuildManifestRejects(t *testing.T) {
	k, other := newKey(t), newKey(t)
	dir := t.TempDir()
	data := []byte("archive")
	for name, tc := range map[string]struct {
		file, comment string
		signer        testKey
		noSig         bool
		why           string
	}{
		"wrong trusted comment": {"sb_1.4.0_linux_amd64.tar.gz", "timestamp:1 file:x", k, false, `want "sb v1.4.0 linux-amd64"`},
		"another key":           {"sb_1.4.0_linux_amd64.tar.gz", "sb v1.4.0 linux-amd64", other, false, "not the update key"},
		"no signature":          {"sb_1.4.0_linux_amd64.tar.gz", "", k, true, "has no signature"},
		"another version":       {"sb_1.3.9_linux_amd64.tar.gz", "sb v1.4.0 linux-amd64", k, false, "not an sb v1.4.0 release archive"},
		"not an archive":        {"checksums.txt", "", k, false, "not an sb"},
	} {
		t.Run(name, func(t *testing.T) {
			var sig []byte
			if !tc.noSig {
				sig = tc.signer.sign(data, tc.comment, false)
			}
			path := writeSigned(t, dir, tc.file, data, sig, ".minisig")
			_, err := BuildManifest(PublishOptions{Version: "v1.4.0", BaseURL: "https://example.com/r", PublicKey: k.pub, Rollout: 100}, []string{path})
			if err == nil || !strings.Contains(err.Error(), tc.why) {
				t.Errorf("err = %v, want %q", err, tc.why)
			}
			_ = os.Remove(path + ".minisig")
		})
	}
}

func TestBuildTrayManifest(t *testing.T) {
	k := newKey(t)
	dir := t.TempDir()
	app := []byte("app bundle")
	exe := []byte("installer")
	b64 := func(b []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(b)) }
	paths := []string{
		writeSigned(t, dir, "Switchboard.app.tar.gz", app, b64(k.sign(app, "timestamp:1\tfile:Switchboard.app.tar.gz", false)), ".sig"),
		writeSigned(t, dir, "Switchboard_1.4.0_x64-setup.exe", exe, b64(k.sign(exe, "timestamp:1\tfile:x", false)), ".sig"),
	}
	m, err := BuildTrayManifest(PublishOptions{Version: "v1.4.0", BaseURL: "https://example.com/r", PublicKey: k.pub, Rollout: 50}, paths)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.4.0" || m.RolloutPercent != 50 || len(m.Platforms) != 3 {
		t.Fatalf("tray manifest %+v", m)
	}
	if p := m.Platforms["darwin-x86_64"]; p.URL != "https://example.com/r/Switchboard.app.tar.gz" || p.Signature == "" {
		t.Errorf("darwin-x86_64 %+v", p)
	}
	if m.Platforms["windows-x86_64"].URL != "https://example.com/r/Switchboard_1.4.0_x64-setup.exe" {
		t.Errorf("windows %+v", m.Platforms["windows-x86_64"])
	}
	// A bundle that doesn't match its signature is refused.
	if err := os.WriteFile(paths[0], []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildTrayManifest(PublishOptions{Version: "v1.4.0", BaseURL: "https://e", PublicKey: k.pub}, paths); err == nil {
		t.Error("tampered bundle accepted")
	}
}
