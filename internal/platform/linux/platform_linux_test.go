package linux

import (
	"crypto/x509"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/pki/pkitest"
	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// fakeSystem answers the commands Platform runs and records every call. It
// never runs anything.
type fakeSystem struct {
	calls    []string
	resolved bool   // systemd-resolved active
	nmConfig string // NetworkManager --print-config output; "" means not installed
	fail     map[string]string
}

func (f *fakeSystem) run(name string, args ...string) ([]byte, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	for prefix, out := range f.fail {
		if strings.HasPrefix(call, prefix) {
			return []byte(out), errors.New("exit status 1")
		}
	}
	switch {
	case call == "systemctl is-active systemd-resolved":
		if f.resolved {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit status 3")
	case strings.HasPrefix(call, "systemctl is-active"):
		return []byte("active\n"), nil
	case call == "NetworkManager --print-config":
		if f.nmConfig == "" {
			return nil, exec.ErrNotFound
		}
		return []byte(f.nmConfig), nil
	}
	return nil, nil
}

// mutating drops the read-only probes from the recorded calls.
func (f *fakeSystem) mutating() []string {
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "systemctl is-active") && c != "NetworkManager --print-config" && c != "systemctl --version" {
			out = append(out, c)
		}
	}
	return out
}

type env struct {
	root string
	p    *Platform
	sys  *fakeSystem
	home string
}

func newEnv(t *testing.T, sys *fakeSystem) *env {
	t.Helper()
	e := &env{root: t.TempDir(), sys: sys, home: "/home/me"}
	for _, d := range []string{"etc", "home/me", "opt/bin", "usr/local"} {
		if err := os.MkdirAll(filepath.Join(e.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.root, "opt/bin/sb"), []byte("#!sb"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.p = New(Options{
		UID: os.Getuid(), Home: e.home, SbPath: "/opt/bin/sb", Root: e.root, Run: sys.run,
		Chown:        func(*os.File, int, int) error { return nil },
		Anchors:      "/usr/local/share/ca-certificates/%s.crt",
		TrustCommand: []string{"update-ca-certificates"},
		NSS:          func() (posix.NSSStore, error) { t.Error("unexpected NSS access"); return nil, errors.New("disabled") },
	})
	return e
}

func (e *env) read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *env) exists(path string) bool {
	_, err := os.Lstat(filepath.Join(e.root, path))
	return err == nil
}

func (e *env) write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(e.root, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.root, path), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectDNS(t *testing.T) {
	for _, tc := range []struct {
		name string
		sys  fakeSystem
		want dnsMode
	}{
		{"resolved", fakeSystem{resolved: true, nmConfig: "[main]\ndns=dnsmasq\n"}, modeResolved},
		{"dnsmasq", fakeSystem{nmConfig: "[main]\n# comment\ndns = dnsmasq\n"}, modeDnsmasq},
		{"NetworkManager without dnsmasq", fakeSystem{nmConfig: "[main]\ndns=default\n"}, modeHosts},
		{"nothing", fakeSystem{}, modeHosts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newEnv(t, &tc.sys).p.detectDNS(); got != tc.want {
				t.Errorf("mode = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResolvedMode(t *testing.T) {
	e := newEnv(t, &fakeSystem{resolved: true})
	if err := e.p.InstallResolver("test", 15353); err != nil {
		t.Fatal(err)
	}
	path := "etc/systemd/resolved.conf.d/switchboard-test.conf"
	want := marker + "[Resolve]\nDNS=127.0.0.1:15353\nDomains=~test\n"
	if got := e.read(t, path); got != want {
		t.Errorf("drop-in =\n%s\nwant\n%s", got, want)
	}
	if err := e.p.InstallResolver("test", 15353); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if got := strings.Join(e.sys.mutating(), "; "); got != "systemctl restart systemd-resolved; systemctl restart systemd-resolved" {
		t.Errorf("calls: %s", got)
	}
	if err := e.p.CheckResolver("test", 15353); err != nil {
		t.Errorf("CheckResolver: %v", err)
	}
	if err := e.p.CheckResolver("test", 1053); err == nil || !strings.Contains(err.Error(), "does not point at 127.0.0.1:1053") {
		t.Errorf("wrong port: %v", err)
	}

	e.sys.calls = nil
	if err := e.p.RemoveResolver("test"); err != nil {
		t.Fatal(err)
	}
	if e.exists(path) || strings.Join(e.sys.mutating(), "; ") != "systemctl restart systemd-resolved" {
		t.Errorf("after remove: exists=%v calls=%v", e.exists(path), e.sys.mutating())
	}
	if e.exists("etc/systemd/resolved.conf.d") {
		t.Error("left the drop-in dir setup made")
	}

	// A dir that was there before setup (NetworkManager ships an empty
	// dnsmasq.d) stays.
	if err := os.MkdirAll(filepath.Join(e.root, "etc/systemd/resolved.conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.p.InstallResolver("test", 15353); err != nil {
		t.Fatal(err)
	}
	if e.exists("etc/systemd/resolved.conf.d/" + createdMarker) {
		t.Error("marked a dir setup didn't make")
	}
	if err := e.p.RemoveResolver("test"); err != nil {
		t.Fatal(err)
	}
	if !e.exists("etc/systemd/resolved.conf.d") {
		t.Error("removed a dir setup didn't make")
	}
	e.sys.calls = nil
	if err := e.p.RemoveResolver("test"); err != nil || len(e.sys.mutating()) != 0 {
		t.Errorf("second remove: %v, calls %v", err, e.sys.mutating())
	}
	if err := e.p.CheckResolver("test", 15353); err == nil || !strings.Contains(err.Error(), "no split DNS") {
		t.Errorf("missing: %v", err)
	}
}

func TestForeignDropinLeftAlone(t *testing.T) {
	e := newEnv(t, &fakeSystem{resolved: true})
	path := "etc/systemd/resolved.conf.d/switchboard-test.conf"
	e.write(t, path, "[Resolve]\nDNS=10.0.0.1\n")
	if err := e.p.InstallResolver("test", 15353); !errors.Is(err, ErrForeignFile) {
		t.Errorf("install err = %v, want ErrForeignFile", err)
	}
	if err := e.p.RemoveResolver("test"); !errors.Is(err, ErrForeignFile) {
		t.Errorf("remove err = %v, want ErrForeignFile", err)
	}
	if e.read(t, path) != "[Resolve]\nDNS=10.0.0.1\n" {
		t.Error("foreign file changed")
	}
	var f interface{ Fix() string }
	if err := e.p.CheckResolver("test", 15353); !errors.As(err, &f) || !strings.Contains(f.Fix(), "Another tool owns .test") {
		t.Errorf("check: %v", err)
	}
}

func TestDnsmasqMode(t *testing.T) {
	sys := &fakeSystem{nmConfig: "[main]\ndns=dnsmasq\n", fail: map[string]string{"nmcli": "old nmcli"}}
	e := newEnv(t, sys)
	if err := e.p.InstallResolver("test", 15353); err != nil {
		t.Fatal(err)
	}
	if got := e.read(t, "etc/NetworkManager/dnsmasq.d/switchboard-test.conf"); got != marker+"server=/test/127.0.0.1#15353\n" {
		t.Errorf("drop-in = %q", got)
	}
	// nmcli failed, so it falls back to reloading NetworkManager.
	if got := strings.Join(sys.mutating(), "; "); got != "nmcli general reload dns-full; systemctl reload NetworkManager" {
		t.Errorf("calls: %s", got)
	}
	if err := e.p.CheckResolver("test", 15353); err != nil {
		t.Errorf("CheckResolver: %v", err)
	}
}

func TestHostsMode(t *testing.T) {
	e := newEnv(t, &fakeSystem{})
	e.write(t, "etc/hosts", "127.0.0.1 localhost\n10.0.0.5 nas") // no trailing newline
	if err := e.p.InstallResolver("test", 15353); err != nil {
		t.Fatal(err)
	}
	if err := e.p.InstallResolver("test", 15353); err != nil {
		t.Fatal(err)
	}
	want := "127.0.0.1 localhost\n10.0.0.5 nas\n" +
		"# BEGIN Switchboard tlds=test; removed by 'sb uninstall'\n127.0.0.1 probe.test\n::1 probe.test\n# END Switchboard\n"
	if got := e.read(t, "etc/hosts"); got != want {
		t.Errorf("hosts =\n%s\nwant\n%s", got, want)
	}
	if len(e.sys.mutating()) != 0 {
		t.Errorf("hosts mode ran %v", e.sys.mutating())
	}
	if err := e.p.CheckResolver("test", 15353); err != nil {
		t.Errorf("CheckResolver: %v", err)
	}

	// The helper's hosts op.
	if err := e.p.syncHosts([]string{"myapp.test", "api.myapp.test", "myapp.test"}); err != nil {
		t.Fatal(err)
	}
	e.write(t, "etc/hosts", e.read(t, "etc/hosts")+"192.168.1.9 printer\n") // a line added after the block survives
	if err := e.p.syncHosts([]string{"myapp.test", "api.myapp.test"}); err != nil {
		t.Fatal(err)
	}
	want = "127.0.0.1 localhost\n10.0.0.5 nas\n" +
		"# BEGIN Switchboard tlds=test; removed by 'sb uninstall'\n" +
		"127.0.0.1 api.myapp.test\n::1 api.myapp.test\n127.0.0.1 myapp.test\n::1 myapp.test\n127.0.0.1 probe.test\n::1 probe.test\n" +
		"# END Switchboard\n192.168.1.9 printer\n"
	if got := e.read(t, "etc/hosts"); got != want {
		t.Errorf("hosts after sync =\n%s\nwant\n%s", got, want)
	}

	// Root must not map names outside the block's TLDs.
	for _, bad := range [][]string{{"google.com"}, {"*.myapp.test"}, {"evil.test\n1.2.3.4 bank.com"}, {"test"}, make([]string, maxHostsNames+1)} {
		if err := e.p.syncHosts(bad); err == nil {
			t.Errorf("syncHosts(%.40q) accepted", bad)
		}
	}
	if !strings.Contains(e.read(t, "etc/hosts"), "127.0.0.1 myapp.test\n") {
		t.Error("rejected sync changed the file")
	}

	if err := e.p.RemoveResolver("test"); err != nil {
		t.Fatal(err)
	}
	if got := e.read(t, "etc/hosts"); got != "127.0.0.1 localhost\n10.0.0.5 nas\n192.168.1.9 printer\n" {
		t.Errorf("hosts after remove = %q", got)
	}
}

func TestSyncHostsWithoutBlockIsNoop(t *testing.T) {
	e := newEnv(t, &fakeSystem{})
	e.write(t, "etc/hosts", "127.0.0.1 localhost\n")
	if err := e.p.syncHosts([]string{"myapp.test"}); err != nil {
		t.Fatal(err)
	}
	if e.read(t, "etc/hosts") != "127.0.0.1 localhost\n" {
		t.Error("hosts changed without a Switchboard block")
	}
}

func TestInstallAndRemoveService(t *testing.T) {
	sys := &fakeSystem{}
	e := newEnv(t, sys)
	if err := e.p.InstallService(); err != nil {
		t.Fatal(err)
	}
	user, _ := posix.UserName(os.Getuid())
	uid := strconv.Itoa(os.Getuid())
	unit := e.read(t, "etc/systemd/system/switchboard-helper.service")
	for _, want := range []string{marker, "ExecStart=/usr/local/libexec/switchboard/sb-helper helper serve --uid " + uid + "\n",
		"RuntimeDirectory=switchboard", "ProtectSystem=strict", "ReadWritePaths=-/etc/hosts", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_CHOWN", "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("helper unit missing %q:\n%s", want, unit)
		}
	}
	if got := e.read(t, "home/me/.config/systemd/user/switchboard.service"); !strings.Contains(got, "ExecStart=/opt/bin/sb daemon\n") || !strings.Contains(got, "WantedBy=default.target") {
		t.Errorf("daemon unit:\n%s", got)
	}
	if e.read(t, "usr/local/libexec/switchboard/sb-helper") != "#!sb" {
		t.Error("helper binary not copied")
	}
	m := "--machine=" + user + "@"
	wantCalls := []string{
		"systemctl daemon-reload", "systemctl enable switchboard-helper.service", "systemctl restart switchboard-helper.service",
		"systemctl --user " + m + " daemon-reload", "systemctl --user " + m + " enable switchboard.service", "systemctl --user " + m + " restart switchboard.service",
	}
	if got := sys.mutating(); strings.Join(got, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("install calls:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantCalls, "\n"))
	}

	sys.calls = nil
	if err := e.p.RemoveService(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"etc/systemd/system/switchboard-helper.service", "home/me/.config/systemd/user/switchboard.service", "usr/local/libexec/switchboard"} {
		if e.exists(path) {
			t.Errorf("%s left behind", path)
		}
	}
	wantCalls = []string{
		"systemctl --user " + m + " disable --now switchboard.service", "systemctl --user " + m + " daemon-reload",
		"systemctl disable --now switchboard-helper.service", "systemctl daemon-reload",
	}
	if got := sys.mutating(); strings.Join(got, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("remove calls:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantCalls, "\n"))
	}
	sys.calls = nil
	if err := e.p.RemoveService(); err != nil || len(sys.mutating()) != 0 {
		t.Errorf("second remove: %v, calls %v", err, sys.mutating())
	}
}

func TestUserSessionMissing(t *testing.T) {
	sys := &fakeSystem{fail: map[string]string{"systemctl --user": "Failed to connect to bus: No medium found"}}
	e := newEnv(t, sys)
	err := e.p.InstallService()
	if err == nil || !strings.Contains(err.Error(), "loginctl enable-linger") || !strings.Contains(err.Error(), "No medium found") {
		t.Errorf("err = %v", err)
	}
}

func TestUserUnitDirMustBeOwned(t *testing.T) {
	e := newEnv(t, &fakeSystem{})
	// A symlinked ~/.config must not let root write elsewhere.
	if err := os.Symlink(filepath.Join(e.root, "etc"), filepath.Join(e.root, "home/me/.config")); err != nil {
		t.Fatal(err)
	}
	if err := e.p.InstallService(); err == nil {
		t.Fatal("installed through a symlinked ~/.config")
	}
	if e.exists("etc/systemd/user") {
		t.Error("wrote through the symlink")
	}
}

func TestValidate(t *testing.T) {
	e := newEnv(t, &fakeSystem{})
	if os.Getuid() > 0 {
		if err := e.p.Validate(); err != nil {
			t.Errorf("valid: %v", err)
		}
	}
	e.write(t, "opt/my bin/sb", "#!sb")
	for name, o := range map[string]Options{
		"root":          {UID: 0, Home: e.home, SbPath: "/opt/bin/sb"},
		"relative":      {UID: 1000, Home: "home/me", SbPath: "/opt/bin/sb"},
		"space in path": {UID: os.Getuid(), Home: e.home, SbPath: "/opt/my bin/sb"},
		"percent":       {UID: os.Getuid(), Home: e.home, SbPath: "/opt/%h/sb"},
	} {
		o.Root, o.Run = e.root, e.sys.run
		if err := New(o).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestInstallPlanNamesMode(t *testing.T) {
	for _, tc := range []struct {
		sys  fakeSystem
		want string
	}{
		{fakeSystem{resolved: true}, "/etc/systemd/resolved.conf.d/switchboard-test.conf so systemd-resolved"},
		{fakeSystem{nmConfig: "dns=dnsmasq"}, "/etc/NetworkManager/dnsmasq.d/switchboard-test.conf"},
		{fakeSystem{}, "Wildcard routes (*.name.test) will NOT resolve"},
	} {
		plan := newEnv(t, &tc.sys).p.InstallPlan("test", 15353)
		if !strings.Contains(plan[0], tc.want) || len(plan) != 4 {
			t.Errorf("plan %q, want first line containing %q", plan, tc.want)
		}
	}
}

func TestLookupHost(t *testing.T) {
	e := newEnv(t, &fakeSystem{})
	e.p.o.Run = func(string, ...string) ([]byte, error) {
		return []byte("127.0.0.1       STREAM probe.test\n127.0.0.1       DGRAM\n::1             STREAM\n"), nil
	}
	if addrs, err := e.p.LookupHost(t.Context(), "probe.test"); err != nil || strings.Join(addrs, ",") != "127.0.0.1,::1" {
		t.Errorf("addrs %v, %v", addrs, err)
	}
	notFound := exec.Command("sh", "-c", "exit 2").Run()
	e.p.o.Run = func(string, ...string) ([]byte, error) { return nil, notFound }
	if addrs, err := e.p.LookupHost(t.Context(), "nope.test"); err != nil || addrs != nil {
		t.Errorf("not found: %v, %v", addrs, err)
	}
}

func TestPortOwner(t *testing.T) {
	e := newEnv(t, &fakeSystem{})
	var args string
	for out, want := range map[string]string{
		`LISTEN 0 511 127.0.0.1:80 0.0.0.0:* users:(("nginx",pid=812,fd=6),("nginx",pid=811,fd=6))` + "\n": "nginx (pid 812)",
		"LISTEN 0 4096 0.0.0.0:80 0.0.0.0:*\n": "", // root's process, hidden
		"":                                     "",
	} {
		e.p.o.Run = func(name string, a ...string) ([]byte, error) {
			args = name + " " + strings.Join(a, " ")
			return []byte(out), nil
		}
		if got, err := e.p.PortOwner(t.Context(), 80); err != nil || got != want {
			t.Errorf("PortOwner(%q) = %q, %v; want %q", out, got, err, want)
		}
	}
	if args != "ss -H -ltnp sport = :80" {
		t.Errorf("ran %q", args)
	}
}

func TestTrust(t *testing.T) {
	ca, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	sys := &fakeSystem{} // never run real commands: they would edit the trust store
	e := newEnv(t, sys)
	anchors := filepath.Join(e.root, "usr/local/share/ca-certificates")
	if err := os.MkdirAll(anchors, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, c *x509.Certificate) string {
		path := filepath.Join(anchors, name)
		if err := os.WriteFile(path, pkitest.PEM(c), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	older := write("switchboard-1.crt", pkitest.CA(t, strconv.Itoa(uid), nil))
	legacy := write("Switchboard_Local_CA_2.crt", pkitest.CA(t, "", func(c *x509.Certificate) {
		c.ExtKeyUsage = nil
		c.Subject.OrganizationalUnit = []string{"me@laptop"}
	}))
	others := write("switchboard-3.crt", pkitest.CA(t, strconv.Itoa(uid+1), nil))
	foreign := write("corp-root.crt", pkitest.CA(t, strconv.Itoa(uid), func(c *x509.Certificate) { c.Subject.CommonName = "Corp Root" }))
	e.p.o.User = "me"

	if err := e.p.TrustCA(ca.CertPath(), ca.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(anchors, "switchboard-"+ca.Cert.SerialNumber.Text(16)+".crt")
	if got, err := os.ReadFile(mine); err != nil || string(got) != string(pkitest.PEM(ca.Cert)) {
		t.Errorf("anchor %s: %v", mine, err)
	}
	for path, want := range map[string]bool{older: false, legacy: false, others: true, foreign: true} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", filepath.Base(path), err == nil, want)
		}
	}
	if strings.Join(sys.calls, ";") != "update-ca-certificates" {
		t.Errorf("calls %v", sys.calls)
	}

	// The anchor name never comes from the subject, which the user controls.
	evil := pkitest.CA(t, strconv.Itoa(uid), func(c *x509.Certificate) { c.Subject.CommonName = "../../../../tmp/x" })
	for name, tc := range map[string]struct{ path, fp string }{
		"missing":        {filepath.Join(t.TempDir(), "missing.pem"), ca.Fingerprint()},
		"wrong print":    {ca.CertPath(), pki.Fingerprint(evil)},
		"traversing CN":  {pkitest.Write(t, evil), pki.Fingerprint(evil)},
		"another user's": {others, ""},
	} {
		if err := e.p.TrustCA(tc.path, tc.fp); err == nil {
			t.Errorf("%s: trusted", name)
		}
	}

	sys.calls = nil
	if err := os.Remove(ca.CertPath()); err != nil { // the CA file is gone: untrust still finds it
		t.Fatal(err)
	}
	if err := e.p.UntrustCA(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err == nil {
		t.Error("CA still trusted after untrust")
	}
	for _, path := range []string{others, foreign} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("untrust removed %s", filepath.Base(path))
		}
	}
	if strings.Join(sys.calls, ";") != "update-ca-certificates --fresh" {
		t.Errorf("calls %v; want update-ca-certificates --fresh", sys.calls)
	}
	sys.calls = nil
	if err := e.p.UntrustCA(""); err != nil || len(sys.calls) != 0 {
		t.Errorf("untrust with nothing trusted: %v, calls %v", err, sys.calls)
	}

	e.p.o.Anchors, e.p.o.TrustCommand = "", nil
	if err := e.p.TrustCA(pkitest.Write(t, ca.Cert), ca.Fingerprint()); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Errorf("no bundle: %v", err)
	}
}
