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
	"strconv"
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

// errNoServerAuth means a CA is not limited to TLS server certificates, as
// CAs made by earlier versions of Switchboard are not.
var errNoServerAuth = errors.New("is not limited to TLS server certificates")

// Subject of every Switchboard CA. Trust stores are searched for it when a CA
// is removed, so a replaced or deleted ca.pem can't leave one trusted.
const (
	CAName = "Switchboard Local CA"
	caOrg  = "Switchboard"
)

// AllowedTLDs are the only TLDs a CA may be limited to: names reserved for
// local use (RFC 2606, 6761, 6762 and ICANN's .internal) that no public site
// can have. The privileged helper refuses to trust a CA for any other.
var AllowedTLDs = []string{"test", "local", "localhost", "internal", "example", "invalid"}

// CA is the local root certificate authority.
type CA struct {
	Cert *x509.Certificate
	key  crypto.Signer
	dir  string // the PKI dir
	// Legacy is set for a CA made before CAs were limited to TLS server
	// certificates. It still works, but 'sb trust' replaces it.
	Legacy bool
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
	err = Validate(cert, time.Now())
	legacy := errors.Is(err, errNoServerAuth)
	if err != nil && !legacy {
		return nil, fmt.Errorf("CA %s: %w", caDir, err)
	}
	return &CA{Cert: cert, key: key, dir: dir, Legacy: legacy}, nil
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

// nextDirName is a PKI dir inside the PKI dir that holds a new CA while it
// is trusted, before it replaces the current one.
const nextDirName = ".next"

// Stage makes a new CA constrained to tlds to replace the one in dir,
// dropping any earlier staged one. It takes effect with Promote.
func Stage(dir string, tlds []string) (*CA, error) {
	next := filepath.Join(dir, nextDirName)
	if err := os.RemoveAll(next); err != nil {
		return nil, fmt.Errorf("stage CA: %w", err)
	}
	return LoadOrCreate(next, tlds)
}

// rename is os.Rename; swapped in tests.
var rename = os.Rename

// Promote replaces the CA in dir with the staged one. Leaves the old CA
// signed are reissued on first use.
func Promote(dir string) error {
	next := filepath.Join(dir, nextDirName)
	if _, err := Load(next); err != nil {
		return fmt.Errorf("replace CA: %w", err)
	}
	old, err := os.MkdirTemp(dir, ".old-*")
	if err != nil {
		return fmt.Errorf("replace CA: %w", err)
	}
	defer func() { _ = os.RemoveAll(old) }()
	if err := rename(filepath.Join(dir, caDirName), filepath.Join(old, caDirName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replace CA: %w", err)
	}
	if err := rename(filepath.Join(next, caDirName), filepath.Join(dir, caDirName)); err != nil {
		// Put the old CA back rather than leave none.
		_ = rename(filepath.Join(old, caDirName), filepath.Join(dir, caDirName))
		return fmt.Errorf("replace CA: %w", err)
	}
	return os.RemoveAll(next)
}

// create writes a new CA into a temp dir and renames it into place, so the
// cert and key always match. If another process wins the race, its CA stays.
func create(dir string, tlds []string) error {
	if len(tlds) == 0 {
		return errors.New("create CA: no TLDs to constrain it to")
	}
	for _, tld := range tlds {
		if !slices.Contains(AllowedTLDs, tld) {
			return fmt.Errorf("create CA: .%s is not a TLD reserved for local use (allowed: %s)", tld, strings.Join(AllowedTLDs, ", "))
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("create CA key: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: CAName, Organization: []string{caOrg}, OrganizationalUnit: []string{OwnerTag(currentUID())}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		// Only TLS server certificates: trust stores and verifiers apply a
		// CA's EKU to everything it signs, so the key can't sign code or mail.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// The key can only vouch for names under our TLDs, and never for IPs,
		// mail addresses or URIs outside the reserved .invalid.
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         slices.Clone(tlds),
		ExcludedIPRanges:            allIPs(),
		PermittedEmailAddresses:     []string{"invalid"},
		PermittedURIDomains:         []string{"invalid"},
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

// ValidateConstraints checks that cert is a self-signed Switchboard CA that
// can only sign TLS server certificates for names under TLDs reserved for
// local use, and no IP addresses, whatever its dates. The privileged helper
// refuses to trust anything else.
func ValidateConstraints(cert *x509.Certificate) error {
	switch {
	case !IsSwitchboardCA(cert):
		return fmt.Errorf("not a self-signed CA named %q", CAName)
	case cert.MaxPathLen != 0 || !cert.MaxPathLenZero:
		return errors.New("may sign other CAs")
	case !cert.PermittedDNSDomainsCritical || len(cert.PermittedDNSDomains) == 0:
		return errors.New("has no critical DNS name constraints")
	case !excludesAllIPs(cert.ExcludedIPRanges) || len(cert.PermittedIPRanges) > 0:
		return errors.New("name constraints do not exclude all IP addresses")
	}
	for _, d := range cert.PermittedDNSDomains {
		if !slices.Contains(AllowedTLDs, d) {
			return fmt.Errorf("name constraint %q is not a TLD reserved for local use", d)
		}
	}
	if !slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) || len(cert.UnknownExtKeyUsage) > 0 {
		return errNoServerAuth
	}
	return nil
}

// ValidateNow is Validate at the current time.
func ValidateNow(cert *x509.Certificate) error { return Validate(cert, time.Now()) }

// ValidateRemovable checks that cert is a Switchboard CA, which is all that
// removing it from a trust store needs: expired CAs and CAs from earlier
// versions must stay removable.
func ValidateRemovable(cert *x509.Certificate) error {
	if !IsSwitchboardCA(cert) {
		return fmt.Errorf("not a self-signed CA named %q", CAName)
	}
	return nil
}

// CheckTrust checks what the privileged helper checks before trusting cert,
// beyond ValidateNow: that it is the CA the user was shown, by its SHA-256
// fingerprint, and that it was made by the user with uid (a SID on Windows).
// Together they stop a process that can write the user's CA file from
// swapping in its own CA while the user confirms.
func CheckTrust(cert *x509.Certificate, fingerprint, uid string) error {
	if fingerprint == "" || !strings.EqualFold(Fingerprint(cert), fingerprint) {
		return fmt.Errorf("the CA's SHA-256 fingerprint is %s, not %s as shown when you confirmed; run 'sb trust' again", Fingerprint(cert), fingerprint)
	}
	if !OwnedBy(cert, uid, "") {
		return fmt.Errorf("the CA was not made by uid %s (it is tagged %q); run 'sb trust' as that user", uid, strings.Join(cert.Subject.OrganizationalUnit, ","))
	}
	return nil
}

// IsSwitchboardCA reports whether cert is a self-signed CA with Switchboard's
// subject. Only such certificates are removed from trust stores.
func IsSwitchboardCA(cert *x509.Certificate) bool {
	return cert.BasicConstraintsValid && cert.IsCA &&
		cert.Subject.CommonName == CAName && slices.Equal(cert.Subject.Organization, []string{caOrg}) &&
		cert.CheckSignatureFrom(cert) == nil
}

// OwnerTag is the organizational unit of the CAs made by the user with the
// given uid (a SID on Windows), so each user's CAs can be told apart in the
// system trust store.
func OwnerTag(uid string) string { return "uid " + uid }

// OwnedBy reports whether cert is a Switchboard CA made by the user with uid,
// or, for CAs from earlier versions, which were tagged "login@host", by the
// user named login.
func OwnedBy(cert *x509.Certificate, uid, login string) bool {
	if !IsSwitchboardCA(cert) || len(cert.Subject.OrganizationalUnit) != 1 {
		return false
	}
	ou := cert.Subject.OrganizationalUnit[0]
	return uid != "" && ou == OwnerTag(uid) || login != "" && strings.HasPrefix(ou, login+"@")
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

// currentUID is the current user's uid, or SID on Windows.
func currentUID() string {
	if u, err := user.Current(); err == nil {
		return u.Uid
	}
	return strconv.Itoa(os.Getuid())
}
