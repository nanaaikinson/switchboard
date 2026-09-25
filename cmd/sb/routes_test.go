package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/api/client"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/pki"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := run(t, args...)
	if err != nil {
		t.Fatalf("sb %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestCLIAgainstDaemon(t *testing.T) {
	dir := configDir(t)
	startDaemon(t, daemonOptions{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer up.Close()
	port := strconv.Itoa(up.Listener.Addr().(*net.TCPAddr).Port)

	if out := mustRun(t, "add", "myapp", port); out != "Added myapp.test -> 127.0.0.1:"+port+"\n" {
		t.Errorf("add: %q", out)
	}
	if out := mustRun(t, "add", "myapp", port); !strings.HasPrefix(out, "Updated myapp.test") {
		t.Errorf("re-add: %q", out)
	}
	if out := mustRun(t, "add", "api.myapp", "1", "--wildcard", "--no-redirect"); !strings.Contains(out, "api.myapp.test (+ *.api.myapp.test)") {
		t.Errorf("add wildcard: %q", out)
	}

	// ls --json until the live upstream is reported up.
	var routes []api.RouteStatus
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := json.Unmarshal([]byte(mustRun(t, "ls", "--json")), &routes); err != nil {
			t.Fatal(err)
		}
		if len(routes) == 2 && routes[0].Health == api.HealthUp && routes[1].Health == api.HealthDown {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never settled: %+v", routes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if routes[1].RedirectHTTPS || !routes[0].RedirectHTTPS {
		t.Errorf("--no-redirect not applied: %+v", routes)
	}

	ls := mustRun(t, "ls")
	for _, want := range []string{"NAME", "PORT", "UPSTREAM", "myapp.test", port, "up", "api.myapp.test (+ *.api.myapp.test)", "down"} {
		if !strings.Contains(ls, want) {
			t.Errorf("ls missing %q:\n%s", want, ls)
		}
	}

	// The proxy really routes the new name.
	c := client.New(controlAddr())
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.DNS.Listening || !st.Proxy.Listening || !st.HTTPS.Listening {
		t.Fatalf("components not listening: dns %+v proxy %+v https %+v", st.DNS, st.Proxy, st.HTTPS)
	}
	_, httpsPort, _ := net.SplitHostPort(st.HTTPS.Addrs[0])

	// Plain HTTP redirects to HTTPS, which serves a certificate from the local CA.
	req, _ := http.NewRequest(http.MethodGet, "http://"+st.Proxy.Addrs[0]+"/x?y=1", nil)
	req.Host = "myapp.test"
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if want := "https://myapp.test:" + httpsPort + "/x?y=1"; resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != want {
		t.Errorf("redirect = %d %q, want 307 %q", resp.StatusCode, resp.Header.Get("Location"), want)
	}
	resp, err = httpsClient(t, dir, st.HTTPS.Addrs[0]).Get("https://myapp.test/")
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	if body.String() != "hello from upstream" || resp.ProtoMajor != 2 {
		t.Errorf("proxied body = %q over %s", body, resp.Proto)
	}

	var opened []string
	realOpen := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = realOpen })
	mustRun(t, "open", "myapp")
	mustRun(t, "open", "deep.api.myapp") // covered by the wildcard
	want := []string{"https://myapp.test:" + httpsPort + "/", "https://deep.api.myapp.test:" + httpsPort + "/"}
	if strings.Join(opened, " ") != strings.Join(want, " ") {
		t.Errorf("opened %v, want %v", opened, want)
	}
	if _, err := run(t, "open", "nope"); err == nil || !strings.Contains(err.Error(), "no route for nope.test") {
		t.Errorf("open nope: %v", err)
	}

	if out := mustRun(t, "rm", "api.myapp"); out != "Removed api.myapp.test\n" {
		t.Errorf("rm: %q", out)
	}
	if _, err := run(t, "rm", "api.myapp"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("rm twice: %v", err)
	}
	cfg, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Name != "myapp.test" {
		t.Errorf("persisted routes = %+v", cfg.Routes)
	}
}

func TestLsWarnsWhenProxyNotListening(t *testing.T) {
	configDir(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	startDaemon(t, daemonOptions{httpAddrs: []string{busy.Addr().String()}})

	out, err := run(t, "ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "warning: Proxy is not listening") || !addrInUse(out) {
		t.Errorf("ls did not warn about the proxy:\n%s", out)
	}
}

func TestCLIWithoutDaemon(t *testing.T) {
	configDir(t)
	for _, args := range [][]string{{"add", "myapp", "7000"}, {"rm", "myapp"}, {"ls"}, {"ls", "--json"}, {"open", "myapp"}} {
		_, err := run(t, args...)
		if !errors.Is(err, client.ErrDaemonNotRunning) || !strings.Contains(err.Error(), "sb daemon") {
			t.Errorf("sb %s: err = %v, want daemon-not-running hint", strings.Join(args, " "), err)
		}
	}
}

// httpsClient trusts the test config dir's CA and dials addr for every request.
func httpsClient(t *testing.T, dir, addr string) *http.Client {
	t.Helper()
	ca, err := pki.Load(filepath.Join(dir, "pki"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
	}}
}
