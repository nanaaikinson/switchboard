package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// Issuer issues leaf certificates signed by the CA on demand, and caches them
// in memory and on disk. It is safe for concurrent use.
type Issuer struct {
	ca      *CA
	dir     string
	nameFor func(host string) string
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

// IssuerOptions configures an Issuer.
type IssuerOptions struct {
	// NameFor maps a TLS server name to the certificate name to serve: the
	// name itself, or "*.<parent>" when a wildcard route covers it. Nil
	// serves every name exactly.
	NameFor func(host string) string
	// Now is the clock; nil is time.Now. For tests.
	Now func() time.Time
}

// NewIssuer returns an issuer for ca that caches leaves in the CA's PKI dir.
func NewIssuer(ca *CA, o IssuerOptions) *Issuer {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Issuer{
		ca: ca, dir: filepath.Join(ca.dir, leafDirName),
		nameFor: o.NameFor, now: o.Now,
		cache: map[string]*tls.Certificate{},
	}
}

// TLSConfig returns a server config that issues certificates on demand.
func (i *Issuer) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: i.GetCertificate}
}

// GetCertificate implements tls.Config.GetCertificate.
func (i *Issuer) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")
	if host == "" {
		return nil, errors.New("pki: client sent no server name; connect by name, like https://myapp.test")
	}
	name := host
	if i.nameFor != nil {
		name = i.nameFor(host)
	}
	return i.Certificate(name)
}

// Certificate returns a valid certificate for name, which is a hostname or a
// "*.<parent>" wildcard under the CA's TLDs. It reuses a cached certificate
// until it has less than RenewBefore left.
func (i *Issuer) Certificate(name string) (*tls.Certificate, error) {
	if err := i.checkName(name); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if c := i.cache[name]; c != nil && i.fresh(c.Leaf, name) {
		return c, nil
	}
	path := i.leafPath(name)
	if c, err := i.readLeaf(path); err == nil && i.fresh(c.Leaf, name) {
		i.cache[name] = c
		return c, nil
	}
	c, pemData, err := i.issue(name)
	if err != nil {
		return nil, err
	}
	if err := writeLeaf(path, pemData); err != nil {
		slog.Warn("pki: cache certificate on disk", "err", err) // still usable from memory
	}
	i.cache[name] = c
	return c, nil
}

func (i *Issuer) checkName(name string) error {
	base, wildcard := strings.CutPrefix(name, "*.")
	switch {
	case !config.ValidHostname(base) || strings.Contains(base, "*"):
		return fmt.Errorf("pki: %q is not a valid hostname", name)
	case !i.ca.Permits(name):
		return fmt.Errorf("pki: %s is outside the CA's names (%s)", name, strings.Join(i.ca.Cert.PermittedDNSDomains, ", "))
	case wildcard && !strings.Contains(base, "."):
		return fmt.Errorf("pki: refusing wildcard %s for a whole TLD", name)
	}
	return nil
}

// fresh reports whether leaf is for exactly name, was signed by the current
// CA, and has more than RenewBefore left.
func (i *Issuer) fresh(leaf *x509.Certificate, name string) bool {
	return leaf != nil && len(leaf.DNSNames) == 1 && leaf.DNSNames[0] == name &&
		leaf.CheckSignatureFrom(i.ca.Cert) == nil &&
		!i.now().Before(leaf.NotBefore) && leaf.NotAfter.Sub(i.now()) > RenewBefore
}

func (i *Issuer) issue(name string) (*tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generate key: %w", err)
	}
	now := i.now()
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: name, Organization: []string{"Switchboard"}},
		DNSNames:     []string{name},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(LeafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, i.ca.Cert, key.Public(), i.ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: issue %s: %w", name, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: issue %s: %w", name, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: encode key: %w", err)
	}
	pemData := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	slog.Debug("pki: issued certificate", "name", name, "expires", leaf.NotAfter)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pemData, nil
}

// leafPath is the cache file for name; "*" becomes "_wildcard". checkName has
// already limited name to LDH labels, so it cannot escape the dir.
func (i *Issuer) leafPath(name string) string {
	return filepath.Join(i.dir, strings.Replace(name, "*", "_wildcard", 1)+".pem")
}

func (i *Issuer) readLeaf(path string) (*tls.Certificate, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path built from a validated hostname under our dir
	if err != nil {
		return nil, err
	}
	c, err := tls.X509KeyPair(data, data)
	if err != nil {
		return nil, fmt.Errorf("pki: read %s: %w", path, err)
	}
	if c.Leaf == nil {
		if c.Leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

func writeLeaf(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".leaf-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
