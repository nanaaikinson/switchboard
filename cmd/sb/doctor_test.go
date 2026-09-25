package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/api/client"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/platform"
)

// fakeDiag stubs the diagnostic methods of platform.Platform. Any other
// method panics.
type fakeDiag struct {
	platform.Platform
	helperErr, resolverErr error
	addrs                  []string
	lookupErr              error
	owners                 map[int]string
}

func (f fakeDiag) HelperRunning(context.Context) error { return f.helperErr }
func (f fakeDiag) CheckResolver(string, int) error     { return f.resolverErr }
func (f fakeDiag) LookupHost(context.Context, string) ([]string, error) {
	return f.addrs, f.lookupErr
}
func (f fakeDiag) PortOwner(_ context.Context, port int) (string, error) { return f.owners[port], nil }

type fixErr struct{ msg, fix string }

func (e fixErr) Error() string { return e.msg }
func (e fixErr) Fix() string   { return e.fix }

func useDiag(t *testing.T, f fakeDiag, ports ...int) {
	t.Helper()
	origP, origPorts := currentPlatform, doctorPorts
	currentPlatform = func() (platform.Platform, platform.Options) { return f, platform.Options{} }
	doctorPorts = ports
	t.Cleanup(func() { currentPlatform, doctorPorts = origP, origPorts })
}

// freePort returns a loopback port with nothing listening on it.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// busyPort returns a loopback port held by a listener until the test ends.
func busyPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func addRoute(t *testing.T, dir, name string, port int) {
	t.Helper()
	c := client.New(filepath.Join(dir, api.SocketName))
	if _, _, err := c.Put(context.Background(), config.Route{Name: name, Port: port}); err != nil {
		t.Fatal(err)
	}
}

func proxyPort(t *testing.T, dir string) int {
	t.Helper()
	st, err := client.New(filepath.Join(dir, api.SocketName)).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	port, ok := portOf(st.Proxy.Addrs[0])
	if !ok {
		t.Fatalf("proxy addrs %v", st.Proxy.Addrs)
	}
	return port
}

func wantLines(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l+"\n") {
			t.Errorf("output missing line %q:\n%s", l, out)
		}
	}
}

func TestDoctorAllPass(t *testing.T) {
	dir := configDir(t)
	startDaemon(t, daemonOptions{})
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	upPort := up.Listener.Addr().(*net.TCPAddr).Port
	addRoute(t, dir, "myapp", upPort)
	px, free := proxyPort(t, dir), freePort(t)
	st := status(t, dir)
	hp, _ := portOf(st.HTTPS.Addrs[0])
	useDiag(t, fakeDiag{addrs: []string{"::1", "127.0.0.1"}}, px, hp, free)
	trustTestCA(t, dir)

	out := mustRun(t, "doctor")
	wantLines(t, out,
		fmt.Sprintf("[PASS] port %d: served by Switchboard on 127.0.0.1:%d", hp, hp),
		"[PASS] helper: running",
		"[PASS] resolver .test: split DNS for .test is in place",
		"[PASS] probe.test: resolves to 127.0.0.1",
		fmt.Sprintf("[PASS] port %d: served by Switchboard on 127.0.0.1:%d", px, px),
		fmt.Sprintf("[PASS] port %d: free (not used by Switchboard)", free),
		fmt.Sprintf("[PASS] myapp.test: upstream 127.0.0.1:%d is up", upPort),
		"All 10 checks passed.",
	)
	for _, want := range []string{"[PASS] local CA: valid until ", "[PASS] https probe.test: trusted certificate, expires "} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.HasPrefix(out, "[PASS] daemon: running ") {
		t.Errorf("daemon line: %q", out)
	}
}

func status(t *testing.T, dir string) api.Status {
	t.Helper()
	st, err := client.New(filepath.Join(dir, api.SocketName)).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// trustTestCA makes doctor's HTTPS probe trust the config dir's CA, standing
// in for the system trust store.
func trustTestCA(t *testing.T, dir string) {
	t.Helper()
	ca, err := pki.Load(filepath.Join(dir, "pki"))
	if err != nil {
		t.Fatal(err)
	}
	orig := doctorRoots
	doctorRoots = x509.NewCertPool()
	doctorRoots.AddCert(ca.Cert)
	t.Cleanup(func() { doctorRoots = orig })
}

func TestDoctorHTTPSNotListening(t *testing.T) {
	configDir(t)
	busy := busyPort(t)
	startDaemon(t, daemonOptions{httpsAddrs: []string{fmt.Sprintf("127.0.0.1:%d", busy)}})
	useDiag(t, fakeDiag{addrs: []string{"127.0.0.1"}}, busy)
	out, _ := run(t, "doctor")
	wantLines(t, out, "       fix: Re-run 'sb setup' so the helper binds port 443 and the daemon restarts.")
	for _, want := range []string{"[FAIL] https probe.test: HTTPS proxy not listening: ", "address already in use",
		fmt.Sprintf("[FAIL] port %d: held by a process only visible with sudo", busy)} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorCAChecks(t *testing.T) {
	dir := configDir(t)
	if _, err := pki.LoadOrCreate(filepath.Join(dir, "pki"), []string{"dev"}); err != nil {
		t.Fatal(err)
	}
	r := checkCA([]string{"test"})
	if r.status != checkFail || r.detail != "cannot sign .test names (limited to dev)" || !strings.Contains(r.fix, "sb untrust") {
		t.Errorf("wrong TLD: %+v", r)
	}
	if r := checkCA([]string{"dev"}); r.status != checkPass {
		t.Errorf("right TLD: %+v", r)
	}
}

func TestDoctorFailures(t *testing.T) {
	dir := configDir(t)
	startDaemon(t, daemonOptions{})
	dead := freePort(t)
	addRoute(t, dir, "api.myapp", dead)
	addRoute(t, dir, "*.w", live(t))
	named, hidden := busyPort(t), busyPort(t)
	useDiag(t, fakeDiag{
		helperErr:   fixErr{"not installed", "Run 'sb setup'."},
		resolverErr: fixErr{"/etc/resolver/test was written by another tool", "Remove it."},
		addrs:       []string{"10.0.0.1"},
		owners:      map[int]string{named: "nginx (pid 42)"},
	}, named, hidden)

	out, err := run(t, "doctor")
	// doctorRoots is nil, so the probe is verified against the system trust
	// store, which does not trust this test's CA.
	if err == nil || err.Error() != "8 of 10 checks failed" {
		t.Fatalf("err = %v, want 8 of 10 checks failed\n%s", err, out)
	}
	wantLines(t, out,
		"[FAIL] helper: not installed",
		"       fix: Run 'sb setup'.",
		"[FAIL] resolver .test: /etc/resolver/test was written by another tool",
		"       fix: Remove it.",
		"[FAIL] probe.test: resolves to 10.0.0.1, want 127.0.0.1",
		"       fix: Fix the resolver file first (see above).",
		"       fix: Run 'sb trust'. If it is already trusted, run 'sb untrust', then 'sb trust'.",
		fmt.Sprintf("[FAIL] port %d: held by nginx (pid 42)", named),
		fmt.Sprintf("       fix: Stop nginx (pid 42) or move it off port %d, then re-run 'sb setup' to restart Switchboard.", named),
		fmt.Sprintf("[FAIL] port %d: held by a process only visible with sudo", hidden),
		fmt.Sprintf("[FAIL] api.myapp.test: nothing listening on 127.0.0.1:%d", dead),
		fmt.Sprintf("       fix: Start the app on port %d, or point the route at another port: sb add api.myapp.test <port>", dead),
		"[FAIL] *.w.test: names under it don't resolve (sb-probe.w.test: [10.0.0.1] <nil>)",
		"       fix: Your split DNS only covers exact names (the /etc/hosts fallback). Enable systemd-resolved, then run 'sb uninstall' and 'sb setup'.",
	)
}

// live starts an upstream that accepts connections until the test ends.
func live(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

func TestDoctorDaemonDown(t *testing.T) {
	configDir(t)
	dead := freePort(t)
	cfg := config.New()
	cfg.Routes = []config.Route{{Name: "myapp.test", Port: dead}}
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	named, hidden := busyPort(t), busyPort(t)
	useDiag(t, fakeDiag{
		helperErr:   fmt.Errorf("helper: %w", errors.ErrUnsupported),
		resolverErr: errors.New("read failed"),
		lookupErr:   errors.New("dscacheutil: boom"),
		owners:      map[int]string{named: "httpd (pid 7)"},
	}, named, hidden)

	out, err := run(t, "doctor")
	if err == nil || err.Error() != "6 of 9 checks failed" {
		t.Fatalf("err = %v, want 6 of 9 checks failed\n%s", err, out)
	}
	wantLines(t, out,
		"[FAIL] daemon: not running",
		"[SKIP] helper: helper: unsupported operation",
		"[FAIL] resolver .test: read failed",
		"       fix: Run 'sb setup'.",
		"[FAIL] probe.test: dscacheutil: boom",
		"       fix: Start the daemon first (see above).",
		"[FAIL] local CA: not created yet",
		"[SKIP] https probe.test: daemon not running",
		fmt.Sprintf("[FAIL] port %d: held by httpd (pid 7)", named),
		fmt.Sprintf("[SKIP] port %d: daemon not running", hidden),
		fmt.Sprintf("[FAIL] myapp.test: nothing listening on 127.0.0.1:%d", dead),
	)
}

func TestDoctorNoRoutes(t *testing.T) {
	configDir(t)
	useDiag(t, fakeDiag{addrs: []string{"127.0.0.1"}})
	out, _ := run(t, "doctor")
	wantLines(t, out, "[SKIP] routes: none; add one with: sb add myapp 3000")
}
