package windows

import (
	"bytes"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

// localMachineRoot is the LocalMachine\Root store ("Trusted Root
// Certification Authorities" for every user). Writing to it needs an
// administrator; unlike CurrentUser\Root, it shows no confirmation dialog.
type localMachineRoot struct{}

func openLocalMachineRoot() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		return 0, err
	}
	h, err := windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM, 0, 0,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE, uintptr(unsafe.Pointer(name))) //nolint:gosec // G103: the API takes the store name as a pointer
	if err != nil {
		return 0, fmt.Errorf("open LocalMachine\\Root: %w; run as administrator", err)
	}
	return h, nil
}

func (localMachineRoot) Add(cert *x509.Certificate) error {
	store, err := openLocalMachineRoot()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(store, 0) //nolint:errcheck // closing
	if len(cert.Raw) == 0 || len(cert.Raw) > math.MaxUint32 {
		return errors.New("empty or oversized certificate")
	}
	ctx, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, &cert.Raw[0], uint32(len(cert.Raw))) //nolint:gosec // G115: length checked above
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}
	defer windows.CertFreeCertificateContext(ctx) //nolint:errcheck // freeing
	var added *windows.CertContext
	if err := windows.CertAddCertificateContextToStore(store, ctx, windows.CERT_STORE_ADD_REPLACE_EXISTING, &added); err != nil {
		return err
	}
	defer windows.CertFreeCertificateContext(added) //nolint:errcheck // freeing
	return setServerAuthOnly(added)
}

// serverAuthEKU is the DER of an enhanced key usage list holding only TLS
// server authentication.
var serverAuthEKU = func() []byte {
	b, err := asn1.Marshal([]asn1.ObjectIdentifier{{1, 3, 6, 1, 5, 5, 7, 3, 1}})
	if err != nil {
		panic(err)
	}
	return b
}()

var procCertSetCertificateContextProperty = windows.NewLazySystemDLL("crypt32.dll").NewProc("CertSetCertificateContextProperty")

// certEnhKeyUsagePropID is CERT_ENHKEY_USAGE_PROP_ID.
const certEnhKeyUsagePropID = 9

// setServerAuthOnly limits what Windows trusts the stored root for to TLS
// server authentication, on top of the CA's own EKU extension.
func setServerAuthOnly(ctx *windows.CertContext) error {
	blob := windows.DataBlob{Size: uint32(len(serverAuthEKU)), Data: &serverAuthEKU[0]}                                                             //nolint:gosec // G115: a few bytes
	r, _, err := procCertSetCertificateContextProperty.Call(uintptr(unsafe.Pointer(ctx)), certEnhKeyUsagePropID, 0, uintptr(unsafe.Pointer(&blob))) //nolint:gosec // G103: Win32 call
	if r == 0 {
		return fmt.Errorf("limit the CA to TLS server authentication: %w", err)
	}
	return nil
}

// List returns the certificates in the store that parse.
func (localMachineRoot) List() ([]*x509.Certificate, error) {
	store, err := openLocalMachineRoot()
	if err != nil {
		return nil, err
	}
	defer windows.CertCloseStore(store, 0) //nolint:errcheck // closing
	var out []*x509.Certificate
	var prev *windows.CertContext
	for {
		ctx, err := windows.CertEnumCertificatesInStore(store, prev)
		if err != nil {
			if errors.Is(err, windows.Errno(windows.CRYPT_E_NOT_FOUND)) {
				return out, nil
			}
			return out, err
		}
		raw := unsafe.Slice(ctx.EncodedCert, ctx.Length) //nolint:gosec // G103: the context owns EncodedCert[:Length]
		if c, err := x509.ParseCertificate(bytes.Clone(raw)); err == nil {
			out = append(out, c)
		}
		prev = ctx
	}
}

// Remove deletes every copy of cert from the store; false if there was none.
func (localMachineRoot) Remove(cert *x509.Certificate) (bool, error) {
	store, err := openLocalMachineRoot()
	if err != nil {
		return false, err
	}
	defer windows.CertCloseStore(store, 0) //nolint:errcheck // closing
	removed := false
	var prev *windows.CertContext
	for {
		ctx, err := windows.CertEnumCertificatesInStore(store, prev)
		if err != nil {
			if errors.Is(err, windows.Errno(windows.CRYPT_E_NOT_FOUND)) {
				return removed, nil
			}
			return removed, err
		}
		raw := unsafe.Slice(ctx.EncodedCert, ctx.Length) //nolint:gosec // G103: the context owns EncodedCert[:Length]
		if !bytes.Equal(raw, cert.Raw) {
			prev = ctx
			continue
		}
		// Deleting frees ctx, so restart the enumeration from the top.
		dup := windows.CertDuplicateCertificateContext(ctx)
		if err := windows.CertDeleteCertificateFromStore(dup); err != nil {
			_ = windows.CertFreeCertificateContext(ctx)
			return removed, fmt.Errorf("remove certificate: %w", err)
		}
		_ = windows.CertFreeCertificateContext(ctx)
		removed, prev = true, nil
	}
}
