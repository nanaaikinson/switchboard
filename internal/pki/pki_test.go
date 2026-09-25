package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newCA(t *testing.T) *CA {
	t.Helper()
	ca, err := LoadOrCreate(t.TempDir(), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func roots(ca *CA) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

func verify(ca *CA, leaf *x509.Certificate, host string) error {
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func TestCreateCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	ca, err := LoadOrCreate(dir, []string{"test", "dev"})
	if err != nil {
		t.Fatal(err)
	}
	c := ca.Cert
	if !c.IsCA || !c.MaxPathLenZero || !c.PermittedDNSDomainsCritical {
		t.Errorf("CA flags: IsCA=%v MaxPathLenZero=%v critical=%v", c.IsCA, c.MaxPathLenZero, c.PermittedDNSDomainsCritical)
	}
	if strings.Join(c.PermittedDNSDomains, ",") != "test,dev" || len(c.ExcludedIPRanges) != 2 {
		t.Errorf("constraints: dns %v, excluded IPs %v", c.PermittedDNSDomains, c.ExcludedIPRanges)
	}
	if _, ok := c.PublicKey.(*ecdsa.PublicKey); !ok || c.PublicKey.(*ecdsa.PublicKey).Curve != elliptic.P256() {
		t.Errorf("CA key is %T, want ECDSA P-256", c.PublicKey)
	}
	if got := c.NotAfter.Sub(c.NotBefore); got < CAValidity || got > CAValidity+2*time.Hour {
		t.Errorf("CA validity %v, want ~10 years", got)
	}

	for name, want := range map[string]os.FileMode{"ca/ca-key.pem": 0o600, "ca/ca.pem": 0o644, "ca": 0o700, ".": 0o700} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode %o, want %o", name, got, want)
		}
	}

	again, err := LoadOrCreate(dir, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint() != ca.Fingerprint() {
		t.Error("LoadOrCreate replaced an existing CA")
	}
	if ca.CertPath() != filepath.Join(dir, "ca", "ca.pem") {
		t.Errorf("CertPath = %s", ca.CertPath())
	}
}

func TestLoadMissingCA(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrNoCA) {
		t.Fatalf("err = %v, want ErrNoCA", err)
	}
}

func TestConcurrentCreateKeepsOneCA(t *testing.T) {
	dir := t.TempDir()
	fps := make([]string, 8)
	var wg sync.WaitGroup
	for i := range fps {
		wg.Go(func() {
			ca, err := LoadOrCreate(dir, []string{"test"})
			if err != nil {
				t.Error(err)
				return
			}
			fps[i] = ca.Fingerprint()
		})
	}
	wg.Wait()
	for _, fp := range fps {
		if fp != fps[0] {
			t.Fatalf("callers got different CAs: %v", fps)
		}
	}
}

func TestCreateRejectsBadTLDs(t *testing.T) {
	for _, tlds := range [][]string{nil, {""}, {"my.test"}, {"*"}, {"Test"}} {
		if _, err := LoadOrCreate(t.TempDir(), tlds); err == nil {
			t.Errorf("tlds %q accepted", tlds)
		}
	}
}

// A certificate for google.com signed by our CA key must not verify: the name
// constraints stop a stolen key from impersonating real sites.
func TestNameConstraintsRejectOtherDomains(t *testing.T) {
	ca := newCA(t)
	for _, host := range []string{"google.com", "test.com", "attest", "127.0.0.1"} {
		if _, err := NewIssuer(ca, IssuerOptions{}).Certificate(host); err == nil {
			t.Errorf("issuer issued %s", host)
		}

		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{
			SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: host},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		if ip := parseIP(host); ip != nil {
			tmpl.IPAddresses = ip
		} else {
			tmpl.DNSNames = []string{host}
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, key.Public(), ca.key)
		if err != nil {
			t.Fatal(err)
		}
		leaf, _ := x509.ParseCertificate(der)
		var cie x509.CertificateInvalidError
		if err := verify(ca, leaf, host); !errors.As(err, &cie) || cie.Reason != x509.CANotAuthorizedForThisName {
			t.Errorf("%s: verify err = %v, want CANotAuthorizedForThisName", host, err)
		}
	}
}

func TestIssueExactName(t *testing.T) {
	ca := newCA(t)
	c, err := NewIssuer(ca, IssuerOptions{}).Certificate("myapp.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(ca, c.Leaf, "myapp.test"); err != nil {
		t.Errorf("verify: %v", err)
	}
	if err := verify(ca, c.Leaf, "api.myapp.test"); err == nil {
		t.Error("exact cert verified for a subdomain")
	}
	if got := c.Leaf.NotAfter.Sub(c.Leaf.NotBefore); got < LeafValidity || got > LeafValidity+2*time.Hour {
		t.Errorf("leaf validity %v, want ~90 days", got)
	}
	if _, ok := c.PrivateKey.(*ecdsa.PrivateKey); !ok {
		t.Errorf("leaf key %T", c.PrivateKey)
	}
}

func TestIssueWildcard(t *testing.T) {
	ca := newCA(t)
	// A wildcard route *.myapp.test: NameFor maps names under it to *.<parent>.
	nameFor := func(host string) string {
		if host != "myapp.test" && strings.HasSuffix(host, ".myapp.test") {
			return "*." + host[strings.IndexByte(host, '.')+1:]
		}
		return host
	}
	iss := NewIssuer(ca, IssuerOptions{NameFor: nameFor})
	for _, tc := range []struct{ sni, certName string }{
		{"a.myapp.test", "*.myapp.test"},
		{"B.MyApp.test.", "*.myapp.test"},
		{"x.deep.myapp.test", "*.deep.myapp.test"},
		{"myapp.test", "myapp.test"},
	} {
		c, err := iss.GetCertificate(&tls.ClientHelloInfo{ServerName: tc.sni})
		if err != nil {
			t.Fatalf("%s: %v", tc.sni, err)
		}
		if got := c.Leaf.DNSNames; len(got) != 1 || got[0] != tc.certName {
			t.Errorf("%s: cert names %v, want %s", tc.sni, got, tc.certName)
		}
		if err := verify(ca, c.Leaf, strings.TrimSuffix(strings.ToLower(tc.sni), ".")); err != nil {
			t.Errorf("%s: verify: %v", tc.sni, err)
		}
	}
	a, _ := iss.Certificate("*.myapp.test")
	b, _ := iss.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.myapp.test"})
	if a != b {
		t.Error("names under one wildcard got different certificates")
	}
	if err := verify(ca, a.Leaf, "x.deep.myapp.test"); err == nil {
		t.Error("*.myapp.test verified for a second-level subdomain")
	}
}

func TestRejectsBadNames(t *testing.T) {
	iss := NewIssuer(newCA(t), IssuerOptions{})
	for _, name := range []string{"", "test", "*.test", "../../etc/x.test", "a/b.test", "*.*.a.test", "a*.test", "a_b.test"} {
		if _, err := iss.Certificate(name); err == nil {
			t.Errorf("issued %q", name)
		}
	}
	if _, err := iss.GetCertificate(&tls.ClientHelloInfo{}); err == nil {
		t.Error("issued without SNI")
	}
}

func TestRenewal(t *testing.T) {
	ca := newCA(t)
	now := time.Now()
	clock := func() time.Time { return now }
	iss := NewIssuer(ca, IssuerOptions{Now: clock})
	first, err := iss.Certificate("myapp.test")
	if err != nil {
		t.Fatal(err)
	}
	serial := func(c *tls.Certificate) string { return c.Leaf.SerialNumber.String() }

	// A second issuer on the same dir reuses the cached file.
	now = now.Add(10 * 24 * time.Hour)
	disk, err := NewIssuer(ca, IssuerOptions{Now: clock}).Certificate("myapp.test")
	if err != nil || serial(disk) != serial(first) {
		t.Fatalf("disk cache: %v, serial %s want %s", err, serial(disk), serial(first))
	}

	// 31 days left: keep. 29 days left: renew.
	now = first.Leaf.NotAfter.Add(-31 * 24 * time.Hour)
	if c, _ := iss.Certificate("myapp.test"); serial(c) != serial(first) {
		t.Error("renewed with 31 days left")
	}
	now = first.Leaf.NotAfter.Add(-29 * 24 * time.Hour)
	renewed, err := iss.Certificate("myapp.test")
	if err != nil {
		t.Fatal(err)
	}
	if serial(renewed) == serial(first) {
		t.Fatal("not renewed with 29 days left")
	}
	if !renewed.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Error("renewed cert does not expire later")
	}
	// The renewal was written to disk too.
	if c, _ := NewIssuer(ca, IssuerOptions{Now: clock}).Certificate("myapp.test"); serial(c) != serial(renewed) {
		t.Error("renewed cert not cached on disk")
	}
	fi, err := os.Stat(filepath.Join(ca.dir, "leaves", "myapp.test.pem"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("leaf file: %v, mode %v", err, fi.Mode())
	}
}

func TestLeafFromOtherCAIsReissued(t *testing.T) {
	ca := newCA(t)
	if _, err := NewIssuer(ca, IssuerOptions{}).Certificate("myapp.test"); err != nil {
		t.Fatal(err)
	}
	// Replace the CA but keep the leaf cache, as after a CA rotation.
	if err := os.RemoveAll(filepath.Join(ca.dir, "ca")); err != nil {
		t.Fatal(err)
	}
	rotated, err := LoadOrCreate(ca.dir, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewIssuer(rotated, IssuerOptions{}).Certificate("myapp.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(rotated, c.Leaf, "myapp.test"); err != nil {
		t.Errorf("stale leaf served after rotation: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	good := newCA(t).Cert
	if err := Validate(good, time.Now()); err != nil {
		t.Fatalf("good CA: %v", err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mk := func(edit func(*x509.Certificate)) *x509.Certificate {
		tmpl := &x509.Certificate{
			SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: "x"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
			PermittedDNSDomainsCritical: true, PermittedDNSDomains: []string{"test"}, ExcludedIPRanges: allIPs(),
		}
		edit(tmpl)
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return c
	}
	for name, c := range map[string]*x509.Certificate{
		"unconstrained":   mk(func(c *x509.Certificate) { c.PermittedDNSDomains = nil }),
		"not critical":    mk(func(c *x509.Certificate) { c.PermittedDNSDomainsCritical = false }),
		"allows IPs":      mk(func(c *x509.Certificate) { c.ExcludedIPRanges = nil }),
		"multi-label":     mk(func(c *x509.Certificate) { c.PermittedDNSDomains = []string{"google.com"} }),
		"not a CA":        mk(func(c *x509.Certificate) { c.IsCA = false }),
		"expired":         mk(func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Minute) }),
		"permits all v4s": mk(func(c *x509.Certificate) { c.PermittedIPRanges = allIPs()[:1] }),
	} {
		if err := Validate(c, time.Now()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func parseIP(s string) []net.IP {
	if ip := net.ParseIP(s); ip != nil {
		return []net.IP{ip}
	}
	return nil
}
