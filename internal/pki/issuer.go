package pki

import (
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// Limits on what any client that can reach the HTTPS port, such as a web page
// looping over random names, can make the issuer do.
const (
	// maxCached is how many leaves are kept in memory; the least recently
	// used go first.
	maxCached = 512
	// Leaves for names no route serves are only kept in memory, and at most
	// unroutedBurst are issued at once, then one per unroutedEvery.
	unroutedBurst = 16
	unroutedEvery = 2 * time.Second
)

// Issuer issues leaf certificates signed by the CA on demand, and caches them
// in memory and, for routed names, on disk. It is safe for concurrent use.
type Issuer struct {
	ca      *CA
	dir     string
	nameFor func(host string) string
	routed  func(host string) bool
	now     func() time.Time
	max     int // leaves kept in memory

	mu       sync.Mutex
	cache    map[string]*list.Element // of *cached, most recently used first
	lru      *list.List
	inflight map[string]*issuing
	tokens   float64 // for unrouted names
	filled   time.Time
}

type cached struct {
	name string
	cert *tls.Certificate
}

// issuing is a certificate being loaded or issued; others asking for the
// same name wait for it.
type issuing struct {
	done chan struct{}
	cert *tls.Certificate
	err  error
}

// IssuerOptions configures an Issuer.
type IssuerOptions struct {
	// NameFor maps a TLS server name to the certificate name to serve: the
	// name itself, or "*.<parent>" when a wildcard route covers it. Nil
	// serves every name exactly.
	NameFor func(host string) string
	// Routed reports whether a route (or the dashboard) serves a TLS server
	// name. Other names still get a certificate, so the not-found page loads
	// over HTTPS, but it is only kept in memory and issued at a limited rate.
	// Nil treats every name as routed.
	Routed func(host string) bool
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
		nameFor: o.NameFor, routed: o.Routed, now: o.Now,
		cache: map[string]*list.Element{}, lru: list.New(), inflight: map[string]*issuing{},
		tokens: unroutedBurst, filled: o.Now(), max: maxCached,
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
	return i.certificate(name, i.routed == nil || i.routed(host))
}

// Certificate returns a valid certificate for name, which is a hostname or a
// "*.<parent>" wildcard under the CA's TLDs. It reuses a cached certificate
// until it has less than RenewBefore left.
func (i *Issuer) Certificate(name string) (*tls.Certificate, error) {
	return i.certificate(name, true)
}

// certificate returns a certificate for name. Routed names are cached on
// disk too; others only in memory, and issued at a limited rate.
func (i *Issuer) certificate(name string, routed bool) (*tls.Certificate, error) {
	if err := i.checkName(name); err != nil {
		return nil, err
	}
	i.mu.Lock()
	if e := i.cache[name]; e != nil && i.fresh(e.Value.(*cached).cert.Leaf, name) {
		i.lru.MoveToFront(e)
		i.mu.Unlock()
		return e.Value.(*cached).cert, nil
	}
	if w := i.inflight[name]; w != nil {
		i.mu.Unlock()
		<-w.done
		return w.cert, w.err
	}
	w := &issuing{done: make(chan struct{})}
	i.inflight[name] = w
	i.mu.Unlock()

	// Keys are made and signed outside the lock, so a slow name doesn't hold
	// up handshakes for others.
	w.cert, w.err = i.load(name, routed)
	i.mu.Lock()
	delete(i.inflight, name)
	if w.err == nil {
		i.put(name, w.cert)
	}
	i.mu.Unlock()
	close(w.done)
	return w.cert, w.err
}

// load reads name's leaf from disk or issues one.
func (i *Issuer) load(name string, routed bool) (*tls.Certificate, error) {
	path := i.leafPath(name)
	if !routed {
		if !i.takeToken() {
			return nil, fmt.Errorf("pki: too many certificates for names without a route; add a route for %s", name)
		}
		c, _, err := i.issue(name)
		return c, err
	}
	if c, err := i.readLeaf(path); err == nil && i.fresh(c.Leaf, name) {
		return c, nil
	}
	c, pemData, err := i.issue(name)
	if err != nil {
		return nil, err
	}
	if err := writeLeaf(path, pemData); err != nil {
		// The error names the file, so the hostname: only at debug level.
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		slog.Warn("pki: could not cache a certificate on disk; it is served from memory", "err", err)
	}
	return c, nil
}

// put caches c for name, dropping the least recently used leaf when full.
// The caller holds i.mu.
func (i *Issuer) put(name string, c *tls.Certificate) {
	if e := i.cache[name]; e != nil {
		e.Value.(*cached).cert = c
		i.lru.MoveToFront(e)
		return
	}
	i.cache[name] = i.lru.PushFront(&cached{name: name, cert: c})
	for i.lru.Len() > i.max {
		old := i.lru.Remove(i.lru.Back()).(*cached)
		delete(i.cache, old.name)
	}
}

// takeToken reports whether a leaf for an unrouted name may be issued now.
func (i *Issuer) takeToken() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := i.now()
	i.tokens = min(unroutedBurst, i.tokens+now.Sub(i.filled).Seconds()/unroutedEvery.Seconds())
	i.filled = now
	if i.tokens < 1 {
		return false
	}
	i.tokens--
	return true
}

// PruneLeaves deletes cached leaf files that are expired, due for renewal or
// not signed by the current CA; they would be reissued anyway.
func (i *Issuer) PruneLeaves() {
	paths, _ := filepath.Glob(filepath.Join(i.dir, "*.pem"))
	for _, path := range paths {
		name := strings.Replace(strings.TrimSuffix(filepath.Base(path), ".pem"), "_wildcard", "*", 1)
		if c, err := i.readLeaf(path); err != nil || !i.fresh(c.Leaf, name) {
			_ = os.Remove(path)
		}
	}
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
