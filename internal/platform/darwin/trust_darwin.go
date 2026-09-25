package darwin

import (
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is how the security tool names keychain items, not a security check
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smallstep/truststore"

	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

const systemKeychain = "/Library/Keychains/System.keychain"

// NSSStore is the part of truststore.NSSTrust Switchboard uses.
type NSSStore = posix.NSSStore

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

// TrustNSS adds the CA to the user's Firefox (NSS) stores. Run as the user.
func (p *Platform) TrustNSS(certPath string) error { return p.nss().Trust(certPath) }

// UntrustNSS removes the CA from the user's Firefox (NSS) stores.
func (p *Platform) UntrustNSS(certPath string) error { return p.nss().Untrust(certPath) }

func (p *Platform) nss() posix.NSS {
	return posix.NSS{
		Open:     p.o.NSS,
		Profiles: []string{filepath.Join(p.o.Home, "Library/Application Support/Firefox/Profiles/*")},
		Install:  "brew install nss",
	}
}

var readCA = posix.ReadCA
