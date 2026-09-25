package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/api/client"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/platform"
	"github.com/nanaaikinson/switchboard/internal/proxy"
)

// configDir points SWITCHBOARD_CONFIG_DIR at a temp dir short enough for a socket.
func configDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(config.EnvConfigDir, dir)
	return dir
}

// startDaemon runs the daemon with opts until the test ends. Empty addresses
// default to ephemeral loopback ports.
func startDaemon(t *testing.T, opts daemonOptions) {
	t.Helper()
	if opts.dnsAddr == "" {
		opts.dnsAddr = "127.0.0.1:0"
	}
	if len(opts.httpAddrs) == 0 {
		opts.httpAddrs = []string{"127.0.0.1:0"}
	}
	if len(opts.httpsAddrs) == 0 {
		opts.httpsAddrs = []string{"127.0.0.1:0"}
	}
	if opts.healthInterval == 0 {
		opts.healthInterval = 20 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	opts.ready = func() { close(ready) }
	done := make(chan error, 1)
	go func() { done <- runDaemon(ctx, opts) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("daemon exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon not ready")
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runDaemon: %v", err)
		}
	})
}

func daemonClient(dir string) *client.Client {
	return client.New(filepath.Join(dir, api.SocketName))
}

func TestDaemonServesAndPersistsRoutes(t *testing.T) {
	dir := configDir(t)
	startDaemon(t, daemonOptions{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer up.Close()
	port := up.Listener.Addr().(*net.TCPAddr).Port

	ctx := context.Background()
	c := daemonClient(dir)
	rs, created, err := c.Put(ctx, config.Route{Name: "myapp", Port: port})
	if err != nil || !created || rs.Name != "myapp.test" {
		t.Fatalf("Put = %+v %v %v", rs, created, err)
	}

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != version || !st.DNS.Listening || !st.Proxy.Listening {
		t.Fatalf("status = %+v", st)
	}

	// The proxy routes the new name to the upstream.
	req, _ := http.NewRequest(http.MethodGet, "http://"+st.Proxy.Addrs[0]+"/", nil)
	req.Host = "myapp.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from upstream" {
		t.Errorf("proxied body = %q", body)
	}

	// The health checker reports the upstream up.
	deadline := time.Now().Add(3 * time.Second)
	for {
		routes, err := c.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(routes) == 1 && routes[0].Health == api.HealthUp {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("health never up: %+v", routes)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cfg, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Name != "myapp.test" || cfg.Routes[0].Port != port {
		t.Errorf("persisted routes = %+v", cfg.Routes)
	}
}

func TestDaemonLoadsPersistedRoutesAndRefusesSecondInstance(t *testing.T) {
	dir := configDir(t)
	if err := config.Save(filepath.Join(dir, config.FileName), &config.Config{
		SchemaVersion: 1, Routes: []config.Route{{Name: "saved.test", Port: 4000}},
	}); err != nil {
		t.Fatal(err)
	}
	startDaemon(t, daemonOptions{})
	routes, err := daemonClient(dir).Routes(context.Background())
	if err != nil || len(routes) != 1 || routes[0].Name != "saved.test" {
		t.Errorf("persisted route not loaded: %+v %v", routes, err)
	}
	err = runDaemon(context.Background(), daemonOptions{dnsAddr: "127.0.0.1:0", httpAddrs: []string{"127.0.0.1:0"}})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second daemon err = %v, want already running", err)
	}
}

func TestDaemonDegradesWhenPortsUnavailable(t *testing.T) {
	dir := configDir(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	startDaemon(t, daemonOptions{httpAddrs: []string{busy.Addr().String()}})

	st, err := daemonClient(dir).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Proxy.Listening || !strings.Contains(st.Proxy.Error, "address already in use") {
		t.Errorf("proxy = %+v, want not listening with bind error", st.Proxy)
	}
	if !st.DNS.Listening {
		t.Errorf("dns = %+v, want listening", st.DNS)
	}
}

func TestDaemonRejectsBadConfig(t *testing.T) {
	dir := configDir(t)
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("schema_version = 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runDaemon(context.Background(), daemonOptions{dnsAddr: "127.0.0.1:0", httpAddrs: []string{"127.0.0.1:0"}})
	if err == nil || !strings.Contains(err.Error(), "upgrade sb") {
		t.Errorf("err = %v, want schema error", err)
	}
	if _, err := os.Stat(filepath.Join(dir, api.SocketName)); !os.IsNotExist(err) {
		t.Error("socket created despite bad config")
	}
}

// hostsSpy records SyncHosts calls; every other method panics.
type hostsSpy struct {
	platform.Platform
	got chan []string
	err error
}

func (h hostsSpy) SyncHosts(_ context.Context, names []string) error { h.got <- names; return h.err }

func TestHostsSyncerSendsExactNames(t *testing.T) {
	px, err := proxy.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &hostsSyncer{Proxy: px, next: make(chan []config.Route, 1)}
	// Two quick changes before the syncer runs: only the latest is sent.
	if err := h.SetRoutes([]config.Route{{Name: "old.test", Port: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := h.SetRoutes([]config.Route{{Name: "myapp.test", Port: 1, Wildcard: true}, {Name: "*.w.test", Port: 2}}); err != nil {
		t.Fatal(err)
	}
	if len(px.Routes()) != 2 {
		t.Fatalf("proxy routes = %v", px.Routes())
	}
	spy := hostsSpy{got: make(chan []string, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.run(ctx, spy); close(done) }()
	select {
	case names := <-spy.got:
		if strings.Join(names, ",") != "myapp.test" {
			t.Errorf("synced %v, want only the exact name myapp.test", names)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no sync")
	}
	cancel()
	<-done

	// An invalid table is rejected and never synced.
	if err := h.SetRoutes([]config.Route{{Name: "bad", Port: 0}}); err == nil {
		t.Error("invalid routes accepted")
	}
	select {
	case rs := <-h.next:
		t.Errorf("invalid table queued: %v", rs)
	default:
	}

	// On an OS that never needs it, the syncer stops.
	_ = h.SetRoutes(nil)
	stopped := make(chan struct{})
	go func() {
		h.run(context.Background(), hostsSpy{got: make(chan []string, 1), err: fmt.Errorf("x: %w", errors.ErrUnsupported)})
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("syncer kept running after ErrUnsupported")
	}
}
