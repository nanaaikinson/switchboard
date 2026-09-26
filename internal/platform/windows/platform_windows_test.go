package windows

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/pki/pkitest"
)

// fakePS answers scripts by what they contain and records them. It never
// runs PowerShell.
type fakePS struct {
	scripts []string
	answer  func(script string) (string, error)
}

func (f *fakePS) run(script string) ([]byte, error) {
	f.scripts = append(f.scripts, script)
	out, err := f.answer(script)
	return []byte(out), err
}

// fakeStore is a certificate store in memory.
type fakeStore struct{ certs []*x509.Certificate }

func (s *fakeStore) Add(c *x509.Certificate) error      { s.certs = append(s.certs, c); return nil }
func (s *fakeStore) List() ([]*x509.Certificate, error) { return slices.Clone(s.certs), nil }
func (s *fakeStore) Remove(c *x509.Certificate) (bool, error) {
	for i, x := range s.certs {
		if x.Equal(c) {
			s.certs = append(s.certs[:i], s.certs[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func newTest(t *testing.T, answer func(string) (string, error)) (*Platform, *fakePS, *fakeStore) {
	t.Helper()
	ps, store := &fakePS{answer: answer}, &fakeStore{}
	sb := filepath.Join(t.TempDir(), "sb.exe")
	if err := os.WriteFile(sb, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(Options{User: `CONTOSO\jane`, SID: "S-1-5-21-1-2-3-1001", SbPath: sb, PowerShell: ps.run, Store: store}), ps, store
}

func ok(string) (string, error) { return "", nil }

func TestValidate(t *testing.T) {
	p, _, _ := newTest(t, ok)
	if err := p.Validate(); err != nil {
		t.Errorf("valid: %v", err)
	}
	for name, o := range map[string]Options{
		"relative": {User: "u", SbPath: `sb.exe`},
		"quote":    {User: "u", SbPath: `C:\a"b\sb.exe`},
		"missing":  {User: "u", SbPath: `C:\nope\sb.exe`},
		"no user":  {SbPath: p.o.SbPath},
	} {
		if err := New(o).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestResolver(t *testing.T) {
	p, ps, _ := newTest(t, ok)
	if err := p.InstallResolver("test", 53); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ps.scripts[0], "Add-DnsClientNrptRule -Namespace '.test'") {
		t.Errorf("script:\n%s", ps.scripts[0])
	}
	if err := p.InstallResolver("test", 15353); err == nil || !strings.Contains(err.Error(), "port 53") {
		t.Errorf("port 15353: %v", err)
	}
	if err := p.InstallResolver("te'st", 53); err == nil {
		t.Error("bad TLD accepted")
	}

	foreign, _, _ := newTest(t, func(string) (string, error) { return "FOREIGN", errors.New("exit status 3") })
	if err := foreign.InstallResolver("test", 53); !errors.Is(err, ErrForeignRule) {
		t.Errorf("foreign rule: %v", err)
	}
	left, _, _ := newTest(t, func(string) (string, error) { return "FOREIGN", nil })
	if err := left.RemoveResolver("test"); !errors.Is(err, ErrForeignRule) {
		t.Errorf("remove with a foreign rule: %v", err)
	}
}

func TestCheckResolver(t *testing.T) {
	ours := `{"Namespace":[".test"],"NameServers":["127.0.0.1"],"Comment":"` + Marker + `"}`
	for name, tc := range map[string]struct {
		out, want string
	}{
		"ours":    {ours, ""},
		"none":    {"", "no NRPT rule"},
		"foreign": {`{"Namespace":[".test"],"NameServers":["10.0.0.9"],"Comment":"vpn"}`, "from another tool"},
		"moved":   {`{"Namespace":[".test"],"NameServers":["10.0.0.9"],"Comment":"` + Marker + `"}`, "doesn't point at 127.0.0.1"},
	} {
		p, _, _ := newTest(t, func(string) (string, error) { return tc.out, nil })
		err := p.CheckResolver("test", 53)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
		var f interface{ Fix() string }
		if tc.want != "" && !errors.As(err, &f) {
			t.Errorf("%s: no fix", name)
		}
	}
}

func TestServiceScripts(t *testing.T) {
	p, ps, _ := newTest(t, ok)
	if err := p.InstallService(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ps.scripts[0], `Register-ScheduledTask -TaskPath '\Switchboard\' -TaskName 'Daemon-S-1-5-21-1-2-3-1001'`) {
		t.Errorf("script:\n%s", ps.scripts[0])
	}
	gone, _, _ := newTest(t, func(string) (string, error) { return "GONE", nil })
	if restarted, err := gone.RestartDaemon(); restarted || err != nil {
		t.Errorf("no task: %v, %v", restarted, err)
	}
	foreign, _, _ := newTest(t, func(string) (string, error) { return "FOREIGN", errors.New("exit status 3") })
	if err := foreign.RemoveService(); !errors.Is(err, ErrForeignRule) {
		t.Errorf("foreign task: %v", err)
	}
}

func TestTrust(t *testing.T) {
	ca, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := CurrentSID() // the CA is tagged with the SID of whoever made it
	if err != nil {
		t.Fatal(err)
	}
	p, _, store := newTest(t, ok)
	p.o.SID = sid
	older := pkitest.CA(t, sid, nil)
	legacy := pkitest.CA(t, "", func(c *x509.Certificate) {
		c.ExtKeyUsage = nil
		c.Subject.OrganizationalUnit = []string{`CONTOSO\jane@laptop`}
	})
	others := pkitest.CA(t, "S-1-5-21-1-2-3-1002", nil)
	store.certs = []*x509.Certificate{older, legacy, others}
	if err := p.TrustCA(ca.CertPath(), ca.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if len(store.certs) != 2 || !store.certs[0].Equal(others) || !store.certs[1].Equal(ca.Cert) {
		t.Fatalf("store after trust: %d certs", len(store.certs))
	}
	for name, tc := range map[string]struct{ path, fp string }{
		"missing":        {filepath.Join(t.TempDir(), "nope.pem"), ca.Fingerprint()},
		"no fingerprint": {ca.CertPath(), ""},
		"swapped":        {pkitest.Write(t, older), ca.Fingerprint()},
		"another user's": {pkitest.Write(t, others), pki.Fingerprint(others)},
		"legacy":         {pkitest.Write(t, legacy), pki.Fingerprint(legacy)},
	} {
		if err := p.TrustCA(tc.path, tc.fp); err == nil {
			t.Errorf("%s: trusted", name)
		}
	}
	store.certs = append(store.certs, legacy)
	if err := os.Remove(ca.CertPath()); err != nil { // the CA file is gone: untrust still finds it
		t.Fatal(err)
	}
	if err := p.UntrustCA(""); err != nil || len(store.certs) != 1 || !store.certs[0].Equal(others) {
		t.Errorf("untrust: %v, %d certs left", err, len(store.certs))
	}
}

func TestPortOwnerParsesListener(t *testing.T) {
	for out, want := range map[string]string{
		"1234\tnginx\tCONTOSO\\jane": "nginx (pid 1234)",
		"1234\tnginx\tcontoso\\JANE": "nginx (pid 1234)",
		"1234\tnginx\tCONTOSO\\bob":  "nginx (pid 1234, run by another account: CONTOSO\\bob)",
		"1234\tdns\t":                "dns (pid 1234, run by another account: an account whose processes you can't see)",
	} {
		p, _, _ := newTest(t, func(string) (string, error) { return out, nil })
		if got, err := p.PortOwner(context.Background(), 53); err != nil || got != want {
			t.Errorf("PortOwner(%q) = %q, %v; want %q", out, got, err, want)
		}
	}
	free, _, _ := newTest(t, ok)
	if got, _ := free.PortOwner(context.Background(), 80); got != "" {
		t.Errorf("free port owner %q", got)
	}
}

func TestHelperNotNeeded(t *testing.T) {
	p, _, _ := newTest(t, ok)
	if err := p.HelperRunning(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("HelperRunning: %v", err)
	}
	if _, err := p.HelperListeners(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("HelperListeners: %v", err)
	}
}
