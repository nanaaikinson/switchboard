package darwin

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is how the security tool names keychain items, not a security check
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/smallstep/truststore"

	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

const systemKeychain = "/Library/Keychains/System.keychain"

// NSSStore is the part of truststore.NSSTrust Switchboard uses.
type NSSStore = posix.NSSStore

func defaultNSS() (NSSStore, error) { return truststore.NewNSSTrust() }

// TrustPlan lists what TrustCA and TrustNSS change.
func (p *Platform) TrustPlan(certPath string) []string {
	return []string{
		fmt.Sprintf("Add %s to %s and trust it for TLS server certificates, replacing any older Switchboard CA of yours (macOS may ask for your password again)", certPath, systemKeychain),
		"As you, not root: add it to Firefox's certificate stores, if Firefox and certutil ('brew install nss') are installed",
	}
}

// UntrustPlan lists what UntrustCA and UntrustNSS change.
func (p *Platform) UntrustPlan(certPath string) []string {
	what := "every Switchboard CA of yours"
	if certPath != "" {
		what = certPath + " and any older Switchboard CA of yours"
	}
	return []string{
		fmt.Sprintf("Remove the trust setting for %s and delete them from %s", what, systemKeychain),
		"As you, not root: remove it from Firefox's certificate stores",
	}
}

// TrustCA adds the CA at certPath to the System keychain as trusted for TLS.
// Privileged. The file is re-validated as root: only a Switchboard CA limited
// to TLS server certificates under reserved TLDs, made by this user and with
// the fingerprint the user confirmed, is trusted.
func (p *Platform) TrustCA(certPath, fingerprint string) error {
	cert, err := readCA(certPath, pki.ValidateNow)
	if err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	if err := pki.CheckTrust(cert, fingerprint, strconv.Itoa(p.o.UID)); err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	// truststore finds trust settings by subject, so older CAs with the same
	// subject go first. The user's other CAs, such as ones from earlier
	// versions, stay trusted until this one is, so HTTPS keeps working if
	// macOS refuses.
	older := func(c *x509.Certificate) bool { return !c.Equal(cert) && p.owns(c) }
	if err := p.removeCAs(func(c *x509.Certificate) bool { return older(c) && bytes.Equal(c.RawSubject, cert.RawSubject) }); err != nil {
		return fmt.Errorf("trust CA: remove older Switchboard CAs: %w", err)
	}
	if err := p.o.Trust(cert); err != nil {
		return fmt.Errorf("trust CA in %s: %w; run 'sb trust' from a logged-in Terminal session so macOS can ask for approval", systemKeychain, err)
	}
	if err := p.removeCAs(older); err != nil {
		return fmt.Errorf("trusted the CA, but could not remove older Switchboard CAs: %w; run 'sb trust' again", err)
	}
	return nil
}

// UntrustCA removes the trust setting and keychain item of every Switchboard
// CA this user made, and of the CA at certPath if given. Privileged. It is a
// no-op if there are none.
func (p *Platform) UntrustCA(certPath string) error {
	var given *x509.Certificate
	if certPath != "" {
		c, err := readCA(certPath, pki.ValidateRemovable) // an expired or older CA must still be removable
		if err != nil {
			return fmt.Errorf("untrust CA: %w", err)
		}
		given = c
	}
	if err := p.removeCAs(func(c *x509.Certificate) bool { return p.owns(c) || given != nil && c.Equal(given) }); err != nil {
		return fmt.Errorf("untrust CA: %w", err)
	}
	return nil
}

// owns reports whether c is a Switchboard CA made by the user.
func (p *Platform) owns(c *x509.Certificate) bool {
	login := p.o.User
	if login == "" {
		login, _ = posix.UserName(p.o.UID)
	}
	return p.o.UID > 0 && pki.OwnedBy(c, strconv.Itoa(p.o.UID), login)
}

// removeCAs removes the Switchboard CAs in the System keychain that match.
func (p *Platform) removeCAs(match func(*x509.Certificate) bool) error {
	out, err := p.o.Run("security", "find-certificate", "-a", "-c", pki.CAName, "-p", systemKeychain)
	if err != nil {
		if strings.Contains(string(out), "could not be found") {
			return nil
		}
		return fmt.Errorf("list %s: %w: %s", systemKeychain, err, out)
	}
	for rest := out; ; {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			return nil
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil || !pki.IsSwitchboardCA(c) || !match(c) {
			continue
		}
		if err := p.removeCA(c); err != nil {
			return err
		}
	}
}

// removeCA removes c's trust setting and deletes it from the System keychain.
func (p *Platform) removeCA(c *x509.Certificate) error {
	// security reads the certificate from a file; hand it a copy of the
	// keychain's, never a path the user could change.
	tmp, err := os.CreateTemp("", "sb-ca-*.pem")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := pem.Encode(tmp, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if out, err := p.o.Run("security", "remove-trusted-cert", "-d", tmp.Name()); err != nil && !strings.Contains(string(out), "could not be found") {
		return fmt.Errorf("remove trust setting: %w: %s", err, out)
	}
	sum := sha1.Sum(c.Raw) //nolint:gosec // G401: keychain item name, see import
	if out, err := p.o.Run("security", "delete-certificate", "-Z", strings.ToUpper(hex.EncodeToString(sum[:])), systemKeychain); err != nil {
		return fmt.Errorf("delete from %s: %w: %s", systemKeychain, err, out)
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
