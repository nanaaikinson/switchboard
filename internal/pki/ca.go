package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// Validity periods.
const (
	CAValidity   = 10 * 365 * 24 * time.Hour
	LeafValidity = 90 * 24 * time.Hour
	RenewBefore  = 30 * 24 * time.Hour // leaves are reissued with this much left
)

// Layout inside the PKI dir.
const (
	caDirName   = "ca"
	caCertName  = "ca.pem"
	caKeyName   = "ca-key.pem"
	leafDirName = "leaves"
)

// ErrNoCA means the CA has not been created yet.
var ErrNoCA = errors.New("no Switchboard CA yet")

// CA is the local root certificate authority.
type CA struct {
	Cert *x509.Certificate
	key  crypto.Signer
	dir  string // the PKI dir
}

// DefaultDir is the PKI dir inside the config dir.
func DefaultDir() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pki"), nil
}

// CertPath is where the CA certificate for the PKI dir lives.
func CertPath(dir string) string { return filepath.Join(dir, caDirName, caCertName) }

// CertPath is the CA certificate file, the one trust stores are given.
func (ca *CA) CertPath() string { return CertPath(ca.dir) }

// Fingerprint is the SHA-256 of the CA certificate, in hex.
func (ca *CA) Fingerprint() string { return Fingerprint(ca.Cert) }

// Fingerprint is the SHA-256 of cert's DER, in hex.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Load reads the CA from dir. It returns an error matching ErrNoCA if there
// is none.
func Load(dir string) (*CA, error) {
	caDir := filepath.Join(dir, caDirName)
	certPEM, err := os.ReadFile(filepath.Join(caDir, caCertName)) //nolint:gosec // G304: path under our own config dir
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w in %s; run 'sb trust' to create and trust it", ErrNoCA, caDir)
	}
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(caDir, caKeyName)) //nolint:gosec // G304: path under our own config dir
	if err != nil {
		return nil, fmt.Errorf("read CA key: %w", err)
	}
	cert, err := ParseCert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("CA %s: %w", caDir, err)
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("CA key %s: %w", caDir, err)
	}
	if !publicKeysEqual(cert.PublicKey, key.Public()) {
		return nil, fmt.Errorf("CA %s: key does not match certificate; run 'sb untrust', delete %s, then run 'sb trust'", caDir, caDir)
	}
	if err := Validate(cert, time.Now()); err != nil {
		return nil, fmt.Errorf("CA %s: %w", caDir, err)
	}
	return &CA{Cert: cert, key: key, dir: dir}, nil
}

// LoadOrCreate loads the CA from dir, creating one constrained to tlds if
// there is none yet. It is safe to call from several processes at once.
func LoadOrCreate(dir string, tlds []string) (*CA, error) {
	ca, err := Load(dir)
	if !errors.Is(err, ErrNoCA) {
		return ca, err
	}
	if err := create(dir, tlds); err != nil {
		return nil, err
	}
	return Load(dir)
}

// create writes a new CA into a temp dir and renames it into place, so the
// cert and key always match. If another process wins the race, its CA stays.
func create(dir string, tlds []string) error {
	if len(tlds) == 0 {
		return errors.New("create CA: no TLDs to constrain it to")
	}
	for _, tld := range tlds {
		if !config.ValidHostname(tld) || strings.Contains(tld, ".") || strings.HasPrefix(tld, "*") {
			return fmt.Errorf("create CA: invalid TLD %q", tld)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("create CA key: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: "Switchboard Local CA", Organization: []string{"Switchboard"}, OrganizationalUnit: []string{owner()}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		// The key can only vouch for names under our TLDs, and never for IPs.
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         slices.Clone(tlds),
		ExcludedIPRanges:            allIPs(),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return fmt.Errorf("create CA: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode CA key: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.MkdirTemp(dir, ".ca-*")
	if err != nil {
		return fmt.Errorf("create CA: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := os.WriteFile(filepath.Join(tmp, caKeyName), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return fmt.Errorf("write CA key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, caCertName), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil { //nolint:gosec // G306: the CA certificate is public
		return fmt.Errorf("write CA: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, caDirName)); err != nil {
		if _, serr := os.Stat(CertPath(dir)); serr == nil {
			return nil // another process created it first
		}
		return fmt.Errorf("install CA: %w", err)
	}
	return nil
}

// Validate checks that cert is a Switchboard-style root that is valid at now.
// See ValidateConstraints.
func Validate(cert *x509.Certificate, now time.Time) error {
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("not valid now (valid %s to %s); run 'sb untrust', delete the CA dir, then run 'sb trust'",
			cert.NotBefore.Format(time.DateOnly), cert.NotAfter.Format(time.DateOnly))
	}
	return ValidateConstraints(cert)
}

// ValidateConstraints checks that cert is a self-signed CA whose name
// constraints allow only single-label TLDs and no IP addresses, whatever its
// dates. The privileged helper refuses to trust anything else.
func ValidateConstraints(cert *x509.Certificate) error {
	switch {
	case !cert.BasicConstraintsValid || !cert.IsCA:
		return errors.New("not a CA certificate")
	case cert.CheckSignatureFrom(cert) != nil:
		return errors.New("not self-signed")
	case !cert.PermittedDNSDomainsCritical || len(cert.PermittedDNSDomains) == 0:
		return errors.New("has no critical DNS name constraints")
	case !excludesAllIPs(cert.ExcludedIPRanges) || len(cert.PermittedIPRanges) > 0:
		return errors.New("name constraints do not exclude all IP addresses")
	}
	for _, d := range cert.PermittedDNSDomains {
		if !config.ValidHostname(d) || strings.Contains(d, ".") || strings.HasPrefix(d, "*") {
			return fmt.Errorf("name constraint %q is not a single-label TLD", d)
		}
	}
	return nil
}

// Permits reports whether the CA's name constraints allow name, which may be
// a "*." wildcard.
func (ca *CA) Permits(name string) bool {
	base := strings.TrimPrefix(name, "*.")
	for _, d := range ca.Cert.PermittedDNSDomains {
		if strings.HasSuffix(base, "."+d) {
			return true
		}
	}
	return false
}

// ParseCert parses the first PEM certificate in data.
func ParseCert(data []byte) (*x509.Certificate, error) {
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil, errors.New("no PEM certificate found")
		}
		if b.Type == "CERTIFICATE" {
			return x509.ParseCertificate(b.Bytes)
		}
	}
}

func parseKey(data []byte) (crypto.Signer, error) {
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil, errors.New("no PEM private key found")
		}
		if b.Type != "PRIVATE KEY" {
			continue
		}
		k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
		if err != nil {
			return nil, err
		}
		s, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("unsupported key type %T", k)
		}
		return s, nil
	}
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	e, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && e.Equal(b)
}

func allIPs() []*net.IPNet {
	return []*net.IPNet{
		{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)},
		{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
	}
}

func excludesAllIPs(ranges []*net.IPNet) bool {
	var v4, v6 bool
	for _, r := range ranges {
		ones, bits := r.Mask.Size()
		v4 = v4 || (ones == 0 && bits == 32)
		v6 = v6 || (ones == 0 && bits == 128)
	}
	return v4 && v6
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return n
}

// owner names the user and machine the CA belongs to, as trust store UIs show
// it: "me@laptop".
func owner() string {
	name := "unknown"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	host, _ := os.Hostname()
	return name + "@" + host
}
