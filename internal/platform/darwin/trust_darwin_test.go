package darwin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/pki"
)

func testCA(t *testing.T) *pki.CA {
	t.Helper()
	ca, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestTrustCA(t *testing.T) {
	ca := testCA(t)
	var trusted []*x509.Certificate
	p := New(Options{Trust: func(c *x509.Certificate) error { trusted = append(trusted, c); return nil }})
	if err := p.TrustCA(ca.CertPath()); err != nil {
		t.Fatal(err)
	}
	if len(trusted) != 1 || !trusted[0].Equal(ca.Cert) {
		t.Fatalf("trusted %v", trusted)
	}

	link := filepath.Join(t.TempDir(), "link.pem")
	if err := os.Symlink(ca.CertPath(), link); err != nil {
		t.Fatal(err)
	}
	// An unconstrained CA: the helper must refuse to trust it.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "evil"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	evil := filepath.Join(t.TempDir(), "evil.pem")
	if err := os.WriteFile(evil, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"symlink": link, "unconstrained": evil, "relative": "ca.pem",
		"missing": filepath.Join(t.TempDir(), "none.pem"), "directory": t.TempDir(),
	} {
		if err := p.TrustCA(path); err == nil {
			t.Errorf("%s: trusted", name)
		}
	}
	if len(trusted) != 1 {
		t.Errorf("trust store called for rejected files: %d calls", len(trusted))
	}

	p = New(Options{Trust: func(*x509.Certificate) error { return errors.New("denied") }})
	if err := p.TrustCA(ca.CertPath()); err == nil || !strings.Contains(err.Error(), "denied") || !strings.Contains(err.Error(), "logged-in Terminal") {
		t.Errorf("err = %v", err)
	}
}

func TestUntrustCA(t *testing.T) {
	ca := testCA(t)
	sum := sha1.Sum(ca.Cert.Raw)
	hash := strings.ToUpper(hex.EncodeToString(sum[:]))
	for _, tc := range []struct {
		name      string
		present   bool
		removeOut string
		wantCalls []string
	}{
		{name: "not in keychain", wantCalls: []string{"find-certificate"}},
		{name: "present", present: true, wantCalls: []string{"find-certificate", "remove-trusted-cert", "delete-certificate -Z " + hash + " " + systemKeychain}},
		{name: "trust already gone", present: true, removeOut: "SecTrustSettingsRemoveTrustSettings: The specified item could not be found in the keychain.",
			wantCalls: []string{"find-certificate", "remove-trusted-cert", "delete-certificate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			p := New(Options{Run: func(name string, args ...string) ([]byte, error) {
				if name != "security" {
					t.Fatalf("ran %s", name)
				}
				calls = append(calls, strings.Join(args, " "))
				switch args[0] {
				case "find-certificate":
					if tc.present {
						return []byte("SHA-1 hash: 0000\nSHA-1 hash: " + hash + "\n"), nil
					}
					return []byte("SHA-1 hash: 0000\n"), nil
				case "remove-trusted-cert":
					if args[2] == ca.CertPath() {
						t.Error("security was given the user's path, not a validated copy")
					}
					if tc.removeOut != "" {
						return []byte(tc.removeOut), errors.New("exit status 1")
					}
				}
				return nil, nil
			}})
			if err := p.UntrustCA(ca.CertPath()); err != nil {
				t.Fatal(err)
			}
			if len(calls) != len(tc.wantCalls) {
				t.Fatalf("calls %q, want %q", calls, tc.wantCalls)
			}
			for i, want := range tc.wantCalls {
				if !strings.HasPrefix(calls[i], want) {
					t.Errorf("call %d = %q, want prefix %q", i, calls[i], want)
				}
			}
		})
	}
}

type fakeNSS struct {
	precheck           error
	exists             bool
	installs, removals int
}

func (f *fakeNSS) PreCheck() error                           { return f.precheck }
func (f *fakeNSS) Exists(*x509.Certificate) bool             { return f.exists }
func (f *fakeNSS) Install(string, *x509.Certificate) error   { f.installs++; return nil }
func (f *fakeNSS) Uninstall(string, *x509.Certificate) error { f.removals++; return nil }
func nssOf(f *fakeNSS) func() (NSSStore, error)              { return func() (NSSStore, error) { return f, nil } }
func noCertutil() (NSSStore, error)                          { return nil, errors.New("certutil not found") }
func platformNSS(home string, nss func() (NSSStore, error)) *Platform {
	return New(Options{Home: home, NSS: nss})
}

func TestTrustNSS(t *testing.T) {
	ca := testCA(t)
	home := t.TempDir()

	f := &fakeNSS{}
	if err := platformNSS(home, nssOf(f)).TrustNSS(ca.CertPath()); err != nil || f.installs != 1 {
		t.Errorf("install: err %v, installs %d", err, f.installs)
	}
	f = &fakeNSS{exists: true}
	if err := platformNSS(home, nssOf(f)).TrustNSS(ca.CertPath()); err != nil || f.installs != 0 {
		t.Errorf("already trusted: err %v, installs %d", err, f.installs)
	}
	f = &fakeNSS{precheck: errors.New("no NSS databases")}
	if err := platformNSS(home, nssOf(f)).TrustNSS(ca.CertPath()); err != nil || f.installs != 0 {
		t.Errorf("no databases: err %v, installs %d", err, f.installs)
	}

	if err := platformNSS(home, noCertutil).TrustNSS(ca.CertPath()); err != nil {
		t.Errorf("no Firefox, no certutil: %v", err)
	}
	mustMkdir(t, filepath.Join(home, "Library/Application Support/Firefox/Profiles/abc.default"))
	if err := platformNSS(home, noCertutil).TrustNSS(ca.CertPath()); err == nil || !strings.Contains(err.Error(), "brew install nss") {
		t.Errorf("Firefox without certutil: %v", err)
	}

	f = &fakeNSS{}
	if err := platformNSS(home, nssOf(f)).UntrustNSS(ca.CertPath()); err != nil || f.removals != 1 {
		t.Errorf("untrust: err %v, removals %d", err, f.removals)
	}
	if err := platformNSS(home, noCertutil).UntrustNSS(ca.CertPath()); err != nil {
		t.Errorf("untrust without certutil: %v", err)
	}
}
