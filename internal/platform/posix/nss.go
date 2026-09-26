//go:build darwin || linux

package posix

import (
	"crypto/x509"
	"fmt"
	"path/filepath"

	"github.com/nanaaikinson/switchboard/internal/pki"
)

// NSSStore is the part of truststore.NSSTrust Switchboard uses.
type NSSStore interface {
	PreCheck() error
	Exists(cert *x509.Certificate) bool
	Install(filename string, cert *x509.Certificate) error
	Uninstall(filename string, cert *x509.Certificate) error
}

// NSS adds and removes the CA in the user's NSS databases (Firefox, and
// Chrome on Linux). Run as the user: the databases live in the user's home
// and must stay owned by them.
type NSS struct {
	Open     func() (NSSStore, error) // fails when certutil is missing
	Profiles []string                 // globs for NSS databases, to tell whether certutil is needed
	Install  string                   // how to install certutil, e.g. "brew install nss"
}

// Trust adds the CA at certPath. It is a no-op when there are no NSS
// databases or the CA is already there.
func (n NSS) Trust(certPath string) error {
	cert, err := ReadCA(certPath, pki.ValidateNow)
	if err != nil {
		return fmt.Errorf("trust CA in NSS: %w", err)
	}
	store, err := n.Open()
	if err != nil {
		if n.hasProfiles() {
			return fmt.Errorf("certutil is missing but browser certificate databases exist; run '%s', then 'sb trust'", n.Install)
		}
		return nil
	}
	if store.PreCheck() != nil || store.Exists(cert) {
		return nil // no NSS databases, or already trusted
	}
	if err := store.Install(certPath, cert); err != nil {
		return fmt.Errorf("trust CA in NSS: %w; quit your browsers and run 'sb trust' again", err)
	}
	return nil
}

// Untrust removes the CA at certPath.
func (n NSS) Untrust(certPath string) error {
	cert, err := ReadCA(certPath, pki.ValidateRemovable)
	if err != nil {
		return fmt.Errorf("untrust CA in NSS: %w", err)
	}
	store, err := n.Open()
	if err != nil || store.PreCheck() != nil {
		return nil // no certutil or no NSS databases: nothing we could have added
	}
	if err := store.Uninstall(certPath, cert); err != nil {
		return fmt.Errorf("untrust CA in NSS: %w; quit your browsers and run 'sb untrust' again", err)
	}
	return nil
}

func (n NSS) hasProfiles() bool {
	for _, g := range n.Profiles {
		if m, _ := filepath.Glob(g); len(m) > 0 {
			return true
		}
	}
	return false
}
