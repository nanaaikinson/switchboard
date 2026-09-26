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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/pki/pkitest"
)

func testCA(t *testing.T) *pki.CA {
	t.Helper()
	ca, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// keychain fakes the security tool over a set of certificates in the System
// keychain.
type keychain struct {
	t     *testing.T
	certs []*x509.Certificate
	calls []string
}

func (k *keychain) run(name string, args ...string) ([]byte, error) {
	if name != "security" {
		k.t.Fatalf("ran %s", name)
	}
	k.calls = append(k.calls, strings.Join(args, " "))
	switch args[0] {
	case "find-certificate":
		var out []byte
		for _, c := range k.certs {
			out = append(out, pkitest.PEM(c)...)
		}
		if len(out) == 0 {
			return []byte("security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain."), errors.New("exit status 44")
		}
		return out, nil
	case "delete-certificate":
		for i, c := range k.certs {
			sum := sha1.Sum(c.Raw)
			if strings.EqualFold(hex.EncodeToString(sum[:]), args[2]) {
				k.certs = append(k.certs[:i], k.certs[i+1:]...)
				return nil, nil
			}
		}
		return []byte("not found"), errors.New("exit status 44")
	}
	return nil, nil
}

func (k *keychain) has(c *x509.Certificate) bool {
	return slices.ContainsFunc(k.certs, c.Equal)
}

func TestTrustCA(t *testing.T) {
	ca := testCA(t)
	uid := os.Getuid()
	var trusted []*x509.Certificate
	older := pkitest.CA(t, strconv.Itoa(uid), nil)
	legacy := pkitest.CA(t, "", func(c *x509.Certificate) {
		c.ExtKeyUsage = nil
		c.Subject.OrganizationalUnit = []string{"me@laptop"}
	})
	others := pkitest.CA(t, strconv.Itoa(uid+1), nil)
	k := &keychain{t: t, certs: []*x509.Certificate{older, legacy, others}}
	p := New(Options{UID: uid, User: "me", Run: k.run, Trust: func(c *x509.Certificate) error { trusted = append(trusted, c); return nil }})
	if err := p.TrustCA(ca.CertPath(), ca.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if len(trusted) != 1 || !trusted[0].Equal(ca.Cert) {
		t.Fatalf("trusted %v", trusted)
	}
	if k.has(older) || k.has(legacy) || !k.has(others) {
		t.Errorf("older CAs of this user should be removed first, another user's kept: calls %q", k.calls)
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
	dotCom := pkitest.CA(t, strconv.Itoa(uid), func(c *x509.Certificate) { c.PermittedDNSDomains = []string{"com"} })
	for name, tc := range map[string]struct{ path, fp string }{
		"symlink":        {link, ca.Fingerprint()},
		"unconstrained":  {evil, ca.Fingerprint()},
		"relative":       {"ca.pem", ca.Fingerprint()},
		"missing":        {filepath.Join(t.TempDir(), "none.pem"), ca.Fingerprint()},
		"directory":      {t.TempDir(), ca.Fingerprint()},
		"swapped":        {pkitest.Write(t, older), ca.Fingerprint()},
		"no fingerprint": {ca.CertPath(), ""},
		"legacy":         {pkitest.Write(t, legacy), pki.Fingerprint(legacy)},
		"public TLD":     {pkitest.Write(t, dotCom), pki.Fingerprint(dotCom)},
		"another user's": {pkitest.Write(t, others), pki.Fingerprint(others)},
	} {
		if err := p.TrustCA(tc.path, tc.fp); err == nil {
			t.Errorf("%s: trusted", name)
		}
	}
	if len(trusted) != 1 {
		t.Errorf("trust store called for rejected files: %d calls", len(trusted))
	}

	// If macOS refuses, the CA being replaced must still be trusted, or
	// HTTPS breaks until the next try.
	k = &keychain{t: t, certs: []*x509.Certificate{legacy}}
	p = New(Options{UID: uid, User: "me", Run: k.run, Trust: func(*x509.Certificate) error { return errors.New("denied") }})
	if err := p.TrustCA(ca.CertPath(), ca.Fingerprint()); err == nil || !strings.Contains(err.Error(), "denied") || !strings.Contains(err.Error(), "logged-in Terminal") {
		t.Errorf("err = %v", err)
	}
	if !k.has(legacy) {
		t.Error("removed the old CA although the new one wasn't trusted")
	}
}

func TestUntrustCA(t *testing.T) {
	ca := testCA(t)
	uid := os.Getuid()
	older := pkitest.CA(t, strconv.Itoa(uid), nil)
	legacy := pkitest.CA(t, "", func(c *x509.Certificate) {
		c.ExtKeyUsage = nil
		c.Subject.OrganizationalUnit = []string{"me@laptop"}
	})
	others := pkitest.CA(t, strconv.Itoa(uid+1), nil)
	notOurs := pkitest.CA(t, strconv.Itoa(uid), func(c *x509.Certificate) { c.Subject.CommonName = "Switchboard Local CA (not)" })
	for _, tc := range []struct {
		name     string
		certPath string
		certs    []*x509.Certificate
		keep     []*x509.Certificate
		removals int
	}{
		{name: "nothing in keychain", certPath: ca.CertPath()},
		{name: "present", certPath: ca.CertPath(), certs: []*x509.Certificate{ca.Cert}, removals: 1},
		{name: "CA file gone", certs: []*x509.Certificate{ca.Cert, older, legacy, others, notOurs}, keep: []*x509.Certificate{others, notOurs}, removals: 3},
		{name: "another user's file", certPath: pkitest.Write(t, others), certs: []*x509.Certificate{others}, removals: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := &keychain{t: t, certs: slices.Clone(tc.certs)}
			p := New(Options{UID: uid, User: "me", Run: k.run})
			if err := p.UntrustCA(tc.certPath); err != nil {
				t.Fatal(err)
			}
			var removed int
			for _, c := range k.calls {
				if strings.HasPrefix(c, "remove-trusted-cert -d ") {
					removed++
					if strings.HasSuffix(c, tc.certPath) && tc.certPath != "" {
						t.Error("security was given the user's path, not a copy")
					}
				}
			}
			if removed != tc.removals || len(k.certs) != len(tc.keep) {
				t.Errorf("removed %d (want %d), left %d (want %d): calls %q", removed, tc.removals, len(k.certs), len(tc.keep), k.calls)
			}
			for _, c := range tc.keep {
				if !k.has(c) {
					t.Errorf("removed %v", c.Subject)
				}
			}
		})
	}

	k := &keychain{t: t}
	if err := New(Options{UID: uid, Run: k.run}).UntrustCA(pkitest.Write(t, notOurs)); err == nil {
		t.Error("untrusted a CA that isn't Switchboard's")
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
