package darwin

import (
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is how the security tool names keychain items, not a security check
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/smallstep/truststore"

	"github.com/nanaaikinson/switchboard/internal/pki"
)

const systemKeychain = "/Library/Keychains/System.keychain"

// NSSStore is the part of truststore.NSSTrust Switchboard uses.
type NSSStore interface {
	PreCheck() error
	Exists(cert *x509.Certificate) bool
	Install(filename string, cert *x509.Certificate) error
	Uninstall(filename string, cert *x509.Certificate) error
}

func defaultNSS() (NSSStore, error) { return truststore.NewNSSTrust() }

// TrustPlan lists what TrustCA and TrustNSS change.
func (p *Platform) TrustPlan(certPath string) []string {
	return []string{
		fmt.Sprintf("Add %s to %s and trust it for TLS server certificates (macOS may ask for your password again)", certPath, systemKeychain),
		"As you, not root: add it to Firefox's certificate stores, if Firefox and certutil ('brew install nss') are installed",
	}
}

// UntrustPlan lists what UntrustCA and UntrustNSS change.
func (p *Platform) UntrustPlan(certPath string) []string {
	return []string{
		fmt.Sprintf("Remove the trust setting for %s and delete it from %s", certPath, systemKeychain),
		"As you, not root: remove it from Firefox's certificate stores",
	}
}

// TrustCA adds the CA at certPath to the System keychain as trusted for TLS.
// Privileged. The file is re-validated as root: only a self-signed CA whose
// name constraints allow nothing but single-label TLDs is trusted.
func (p *Platform) TrustCA(certPath string) error {
	cert, err := readCA(certPath, true)
	if err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	if err := p.o.Trust(cert); err != nil {
		return fmt.Errorf("trust CA in %s: %w; run 'sb trust' from a logged-in Terminal session so macOS can ask for approval", systemKeychain, err)
	}
	return nil
}

// UntrustCA removes the CA's trust setting and deletes it from the System
// keychain. Privileged. It is a no-op if the CA is not in the keychain.
func (p *Platform) UntrustCA(certPath string) error {
	cert, err := readCA(certPath, false) // an expired CA must still be removable
	if err != nil {
		return fmt.Errorf("untrust CA: %w", err)
	}
	sum := sha1.Sum(cert.Raw) //nolint:gosec // G401: keychain item name, see import
	hash := strings.ToUpper(hex.EncodeToString(sum[:]))
	out, err := p.o.Run("security", "find-certificate", "-a", "-Z", systemKeychain)
	if err != nil {
		return fmt.Errorf("untrust CA: list %s: %w: %s", systemKeychain, err, out)
	}
	if !strings.Contains(string(out), "SHA-1 hash: "+hash) {
		return nil
	}
	// security reads the certificate from a file; hand it our validated
	// copy, not the user's path, which could change after readCA.
	tmp, err := os.CreateTemp("", "sb-ca-*.pem")
	if err != nil {
		return fmt.Errorf("untrust CA: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := pem.Encode(tmp, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("untrust CA: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("untrust CA: %w", err)
	}
	if out, err := p.o.Run("security", "remove-trusted-cert", "-d", tmp.Name()); err != nil && !strings.Contains(string(out), "could not be found") {
		return fmt.Errorf("untrust CA: remove trust setting: %w: %s", err, out)
	}
	if out, err := p.o.Run("security", "delete-certificate", "-Z", hash, systemKeychain); err != nil {
		return fmt.Errorf("untrust CA: delete from %s: %w: %s", systemKeychain, err, out)
	}
	return nil
}

// TrustNSS adds the CA to the user's Firefox (NSS) stores. Run as the user:
// NSS databases live in the user's profile and must stay owned by them. It
// is a no-op when there are no NSS databases.
func (p *Platform) TrustNSS(certPath string) error {
	cert, err := readCA(certPath, true)
	if err != nil {
		return fmt.Errorf("trust CA in Firefox: %w", err)
	}
	nss, err := p.o.NSS()
	if err != nil {
		if p.hasFirefox() {
			return errors.New("certutil is missing but Firefox is installed; run 'brew install nss', then 'sb trust'")
		}
		return nil
	}
	if nss.PreCheck() != nil || nss.Exists(cert) {
		return nil // no NSS databases, or already trusted
	}
	if err := nss.Install(certPath, cert); err != nil {
		return fmt.Errorf("trust CA in Firefox: %w; quit Firefox and run 'sb trust' again", err)
	}
	return nil
}

// UntrustNSS removes the CA from the user's Firefox (NSS) stores.
func (p *Platform) UntrustNSS(certPath string) error {
	cert, err := readCA(certPath, false)
	if err != nil {
		return fmt.Errorf("untrust CA in Firefox: %w", err)
	}
	nss, err := p.o.NSS()
	if err != nil || nss.PreCheck() != nil {
		return nil // no certutil or no NSS databases: nothing we could have added
	}
	if err := nss.Uninstall(certPath, cert); err != nil {
		return fmt.Errorf("untrust CA in Firefox: %w; quit Firefox and run 'sb untrust' again", err)
	}
	return nil
}

func (p *Platform) hasFirefox() bool {
	m, _ := filepath.Glob(filepath.Join(p.o.Home, "Library/Application Support/Firefox/Profiles/*"))
	return len(m) > 0
}

// readCA reads and validates a CA certificate without following symlinks,
// since the privileged helper reads it from the user's config dir. With
// current set, the certificate must also be valid now.
func readCA(path string, current bool) (*x509.Certificate, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("CA path %q is not absolute", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: validated below; symlinks refused
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cert, err := pki.ParseCert(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	validate := pki.ValidateConstraints
	if current {
		validate = func(c *x509.Certificate) error { return pki.Validate(c, time.Now()) }
	}
	if err := validate(cert); err != nil {
		return nil, fmt.Errorf("%s is not a Switchboard CA: %w", path, err)
	}
	return cert, nil
}
