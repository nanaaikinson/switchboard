package linux

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// anchorPrefix names Switchboard's files in the system CA anchor dir:
// switchboard-<serial in hex>, never anything from the certificate's subject.
const anchorPrefix = "switchboard-"

// TrustCA adds the CA at certPath to the distro CA bundle: it writes an
// anchor file and runs update-ca-certificates, update-ca-trust or trust
// extract-compat. Privileged. The file is re-validated as root: only a
// Switchboard CA limited to TLS server certificates under reserved TLDs, made
// by this user and with the fingerprint the user confirmed, is trusted.
func (p *Platform) TrustCA(certPath, fingerprint string) error {
	cert, err := posix.ReadCA(certPath, pki.ValidateNow)
	if err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	if err := pki.CheckTrust(cert, fingerprint, strconv.Itoa(p.o.UID)); err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	if p.o.Anchors == "" || len(p.o.TrustCommand) == 0 {
		return fmt.Errorf("trust CA: no supported system CA bundle found; add %s to your distro's trust store by hand", certPath)
	}
	anchor := fmt.Sprintf(p.o.Anchors, anchorPrefix+cert.SerialNumber.Text(16))
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := p.files.WriteFile(anchor, data, 0o644, 0, 0); err != nil {
		return fmt.Errorf("trust CA in the system bundle: %w", err)
	}
	// The user's older CAs go once this one is in, in the same regeneration.
	if _, err := p.removeCAs(func(c *x509.Certificate) bool { return !c.Equal(cert) && p.owns(c) }); err != nil {
		return fmt.Errorf("trust CA: remove older Switchboard CAs: %w", err)
	}
	return p.run("regenerate the system CA bundle", p.o.TrustCommand[0], p.o.TrustCommand[1:]...)
}

// UntrustCA removes every Switchboard CA this user made from the distro CA
// bundle, and the CA at certPath if given, then regenerates the bundle.
// Privileged. It is safe to run when there are none.
func (p *Platform) UntrustCA(certPath string) error {
	var given *x509.Certificate
	if certPath != "" {
		c, err := posix.ReadCA(certPath, pki.ValidateRemovable) // an expired or older CA must still be removable
		if err != nil {
			return fmt.Errorf("untrust CA: %w", err)
		}
		given = c
	}
	n, err := p.removeCAs(func(c *x509.Certificate) bool { return p.owns(c) || given != nil && c.Equal(given) })
	if err != nil {
		return fmt.Errorf("untrust CA in the system bundle: %w", err)
	}
	if n == 0 || len(p.o.TrustCommand) == 0 {
		return nil
	}
	cmd := slices.Clone(p.o.TrustCommand)
	// Debian's update-ca-certificates leaves dangling links to a removed
	// local CA in /etc/ssl/certs; --fresh rebuilds them from scratch.
	if cmd[0] == "update-ca-certificates" && strings.HasPrefix(p.o.Anchors, "/usr/local/share/ca-certificates/") {
		cmd = append(cmd, "--fresh")
	}
	return p.run("regenerate the system CA bundle", cmd[0], cmd[1:]...)
}

// owns reports whether c is a Switchboard CA made by the user.
func (p *Platform) owns(c *x509.Certificate) bool {
	login := p.o.User
	if login == "" {
		login, _ = posix.UserName(p.o.UID)
	}
	return p.o.UID > 0 && pki.OwnedBy(c, strconv.Itoa(p.o.UID), login)
}

// removeCAs deletes the anchor files of the Switchboard CAs that match,
// whatever their names: earlier versions named them after the CA's subject.
// It reports how many it removed.
func (p *Platform) removeCAs(match func(*x509.Certificate) bool) (int, error) {
	if p.o.Anchors == "" {
		return 0, nil
	}
	paths, err := filepath.Glob(p.fs(fmt.Sprintf(p.o.Anchors, "*")))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, path := range paths {
		c, err := readAnchor(path)
		if err != nil || !pki.IsSwitchboardCA(c) || !match(c) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return n, fmt.Errorf("remove %s: %w", path, err)
		}
		n++
	}
	return n, nil
}

// readAnchor parses a system anchor file, which may hold anything.
func readAnchor(path string) (*x509.Certificate, error) {
	f, err := os.Open(path) //nolint:gosec // G304: files in the root-owned anchor dir
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return nil, err
	}
	return pki.ParseCert(data)
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
