package linux

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/smallstep/truststore"

	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// TrustCA adds the CA at certPath to the distro CA bundle (via truststore:
// update-ca-certificates, update-ca-trust or trust extract-compat).
// Privileged. The file is re-validated as root first.
func (p *Platform) TrustCA(certPath string) error {
	cert, err := posix.ReadCA(certPath, true)
	if err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	if err := p.o.Trust(cert); err != nil {
		if errors.Is(err, truststore.ErrNotSupported) {
			return fmt.Errorf("trust CA: no supported system CA bundle found; add %s to your distro's trust store by hand", certPath)
		}
		return fmt.Errorf("trust CA in the system bundle: %w", err)
	}
	return nil
}

// UntrustCA removes the CA from the distro CA bundle. Privileged. It is safe
// to run when the CA is not there.
func (p *Platform) UntrustCA(certPath string) error {
	cert, err := posix.ReadCA(certPath, false) // an expired CA must still be removable
	if err != nil {
		return fmt.Errorf("untrust CA: %w", err)
	}
	if err := p.o.Untrust(cert); err != nil && !errors.Is(err, truststore.ErrNotSupported) {
		return fmt.Errorf("untrust CA in the system bundle: %w", err)
	}
	// Debian's update-ca-certificates leaves dangling links to a removed
	// local CA in /etc/ssl/certs; --fresh rebuilds them from scratch.
	if strings.HasPrefix(truststore.SystemTrustFilename, "/usr/local/share/ca-certificates/") {
		return p.run("update-ca-certificates --fresh", "update-ca-certificates", "--fresh")
	}
	return nil
}

// TrustNSS adds the CA to the user's NSS databases: Chrome and Chromium
// (~/.pki/nssdb) and Firefox profiles. Run as the user.
func (p *Platform) TrustNSS(certPath string) error { return p.nss().Trust(certPath) }

// UntrustNSS removes the CA from the user's NSS databases.
func (p *Platform) UntrustNSS(certPath string) error { return p.nss().Untrust(certPath) }

func (p *Platform) nss() posix.NSS {
	return posix.NSS{
		Open: p.o.NSS,
		Profiles: []string{
			filepath.Join(p.o.Home, ".pki/nssdb"),
			filepath.Join(p.o.Home, ".mozilla/firefox/*"),
			filepath.Join(p.o.Home, "snap/firefox/common/.mozilla/firefox/*"),
		},
		Install: "sudo apt install libnss3-tools' or 'sudo dnf install nss-tools",
	}
}
