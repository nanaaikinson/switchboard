//go:build darwin || linux

package posix

import (
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/nanaaikinson/switchboard/internal/pki"
)

// ReadCA reads a CA certificate without following symlinks, since the
// privileged helper reads it from the user's config dir, and checks it with
// validate: pki.ValidateNow before trusting it, pki.ValidateRemovable before
// removing it.
func ReadCA(path string, validate func(*x509.Certificate) error) (*x509.Certificate, error) {
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
	if err := validate(cert); err != nil {
		return nil, fmt.Errorf("%s is not a Switchboard CA: %w", path, err)
	}
	return cert, nil
}
