package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

type fakeProxy struct {
	mu     sync.Mutex
	routes []config.Route
}

func (f *fakeProxy) SetRoutes(r []config.Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = slices.Clone(r)
	return nil
}

func (f *fakeProxy) get() []config.Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.routes)
}

func newTestService(t *testing.T, routes ...config.Route) (*Service, *fakeProxy, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), config.FileName)
	fp := &fakeProxy{}
	s, err := NewService(Options{
		ConfigPath: path, Config: &config.Config{SchemaVersion: 1, Routes: routes},
		Proxy: fp, TLDs: []string{"test"}, Version: "v9.9.9", HealthInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s, fp, path
}

func loadRoutes(t *testing.T, path string) []config.Route {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg.Routes
}

func TestPutQualifiesPersistsAndUpserts(t *testing.T) {
	s, fp, path := newTestService(t)
	tests := []struct {
		in          config.Route
		wantName    string
		wantCreated bool
	}{
		{config.Route{Name: "myapp", Port: 7000}, "myapp.test", true},
		{config.Route{Name: "MyApp.Test", Port: 7001, RedirectHTTPS: true}, "myapp.test", false},
		{config.Route{Name: "*.tenants.myapp", Port: 3000}, "*.tenants.myapp.test", true},
		{config.Route{Name: "api.myapp", Port: 7002, Wildcard: true}, "api.myapp.test", true},
	}
	for _, tt := range tests {
		rs, created, err := s.Put(tt.in)
		if err != nil {
			t.Fatalf("Put(%+v): %v", tt.in, err)
		}
		if rs.Name != tt.wantName || created != tt.wantCreated {
			t.Errorf("Put(%q) = %q created=%v, want %q created=%v", tt.in.Name, rs.Name, created, tt.wantName, tt.wantCreated)
		}
	}
	want := []config.Route{
		{Name: "myapp.test", Port: 7001, RedirectHTTPS: true},
		{Name: "*.tenants.myapp.test", Port: 3000},
		{Name: "api.myapp.test", Port: 7002, Wildcard: true},
	}
	if got := loadRoutes(t, path); !reflect.DeepEqual(got, want) {
		t.Errorf("persisted = %+v\nwant %+v", got, want)
	}
	if got := fp.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("proxy routes = %+v\nwant %+v", got, want)
	}
}

func TestPutRejectsInvalid(t *testing.T) {
	s, _, path := newTestService(t)
	for _, r := range []config.Route{
		{Name: "", Port: 1},
		{Name: "bad name", Port: 1},
		{Name: "a_b", Port: 1},
		{Name: "a.*.b", Port: 1},
		{Name: "ok", Port: 0},
		{Name: "ok", Port: 65536},
	} {
		if _, _, err := s.Put(r); !errors.Is(err, ErrInvalid) {
			t.Errorf("Put(%+v) err = %v, want ErrInvalid", r, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("config written despite only invalid changes")
	}
}

func TestDelete(t *testing.T) {
	s, fp, path := newTestService(t, config.Route{Name: "myapp.test", Port: 7000}, config.Route{Name: "api.myapp.test", Port: 7001})
	if _, err := s.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(nope) err = %v, want ErrNotFound", err)
	}
	gone, err := s.Delete("api.myapp") // qualified to api.myapp.test
	if err != nil {
		t.Fatal(err)
	}
	if gone.Name != "api.myapp.test" {
		t.Errorf("deleted %q", gone.Name)
	}
	want := []config.Route{{Name: "myapp.test", Port: 7000}}
	if got := loadRoutes(t, path); !reflect.DeepEqual(got, want) {
		t.Errorf("persisted = %+v, want %+v", got, want)
	}
	if got := fp.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("proxy = %+v, want %+v", got, want)
	}
}

func TestSaveFailureRollsBack(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs unix permissions as non-root")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	old := []config.Route{{Name: "myapp.test", Port: 7000}}
	fp := &fakeProxy{}
	s, err := NewService(Options{
		ConfigPath: filepath.Join(dir, config.FileName), Config: &config.Config{SchemaVersion: 1, Routes: old},
		Proxy: fp, TLDs: []string{"test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Put(config.Route{Name: "new", Port: 1}); err == nil {
		t.Fatal("Put succeeded on read-only config dir")
	}
	if got := fp.get(); !reflect.DeepEqual(got, old) {
		t.Errorf("proxy not rolled back: %+v", got)
	}
	if got := s.Routes(); len(got) != 1 || got[0].Name != "myapp.test" {
		t.Errorf("service routes changed: %+v", got)
	}
}

func TestHealthUpDownAndEvents(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	upPort := up.Listener.Addr().(*net.TCPAddr).Port
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	s, _, _ := newTestService(t,
		config.Route{Name: "up.test", Port: upPort},
		config.Route{Name: "down.test", Port: closedPort})
	events, cancelSub := s.hub.subscribe()
	defer cancelSub()
	ctx := t.Context()
	go s.Run(ctx)

	waitHealth(t, s, "up.test", HealthUp)
	waitHealth(t, s, "down.test", HealthDown)

	up.Close()
	waitHealth(t, s, "up.test", HealthDown)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == EventHealthChanged && e.Route.Name == "up.test" && e.Route.Health == HealthDown {
				return
			}
		case <-deadline:
			t.Fatal("no health.changed event for up.test going down")
		}
	}
}

func waitHealth(t *testing.T, s *Service, name string, want Health) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range s.Routes() {
			if r.Name == name && r.Health == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never became %s: %+v", name, want, s.Routes())
}

func TestHTTPHandlers(t *testing.T) {
	s, _, _ := newTestService(t)
	s.SetDNS(Listener{Addrs: []string{"127.0.0.1:15353"}, Listening: true})
	s.SetProxy(Listener{Addrs: []string{"127.0.0.1:80"}, Error: "bind: permission denied"})
	srv := httptest.NewServer(Handler(s))
	defer srv.Close()

	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	tests := []struct {
		method, path, body string
		wantCode           int
		wantBody           string
	}{
		{"POST", "/v1/routes", `{"name":"myapp","port":7000}`, 201, `"name":"myapp.test"`},
		{"POST", "/v1/routes", `{"name":"myapp","port":7001}`, 200, `"port":7001`},
		{"POST", "/v1/routes", `{"name":"x","port":1,"bogus":true}`, 400, `unknown field`},
		{"POST", "/v1/routes", `not json`, 400, `"error"`},
		{"POST", "/v1/routes", `{"name":"bad name","port":1}`, 400, `must be a hostname`},
		{"GET", "/v1/routes", "", 200, `"health":"`},
		{"GET", "/v1/status", "", 200, `"version":"v9.9.9"`},
		{"GET", "/v1/status", "", 200, `"error":"bind: permission denied"`},
		{"GET", "/v1/status", "", 200, `"tlds":["test"]`},
		{"DELETE", "/v1/routes/nope", "", 404, `not found`},
		{"DELETE", "/v1/routes/myapp", "", 200, `"name":"myapp.test"`},
		{"GET", "/v1/routes", "", 200, `[]`},
		{"GET", "/v2/routes", "", 404, ``},
		{"PUT", "/v1/routes", "", 405, ``},
	}
	for _, tt := range tests {
		code, body := do(tt.method, tt.path, tt.body)
		if code != tt.wantCode || !strings.Contains(body, tt.wantBody) {
			t.Errorf("%s %s %s = %d %s; want %d containing %q", tt.method, tt.path, tt.body, code, body, tt.wantCode, tt.wantBody)
		}
	}
}

func TestEventsStream(t *testing.T) {
	s, _, _ := newTestService(t)
	srv := httptest.NewServer(Handler(s))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	br := bufio.NewReader(resp.Body)
	if line, _ := br.ReadString('\n'); line != ": connected\n" {
		t.Fatalf("first line = %q", line)
	}

	if _, _, err := s.Put(config.Route{Name: "myapp", Port: 7000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete("myapp"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{EventRouteAdded, EventRouteRemoved} {
		typ, data := readEvent(t, br)
		if typ != want {
			t.Fatalf("event = %q, want %q", typ, want)
		}
		var e Event
		if err := json.Unmarshal([]byte(data), &e); err != nil || e.Route.Name != "myapp.test" || e.Type != want {
			t.Errorf("data = %s (err %v)", data, err)
		}
	}
}

// readEvent skips comments and health events and returns the next event.
func readEvent(t *testing.T, br *bufio.Reader) (typ, data string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && typ != "" && typ != EventHealthChanged:
				return
			case line == "":
				typ, data = "", ""
			}
		}
	}()
	select {
	case <-done:
		return typ, data
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
		return "", ""
	}
}

// shortDir returns a temp dir short enough for a Unix socket path.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListenUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("socket file modes are unix-only")
	}
	path := filepath.Join(shortDir(t), SocketName)
	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 600", got)
	}
	if _, err := ListenUnix(path); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second ListenUnix err = %v, want already running", err)
	}

	// Simulate a crash: the socket file stays but nothing listens.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	ln2, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("ListenUnix over stale socket: %v", err)
	}
	_ = ln2.Close()

	long := filepath.Join(shortDir(t), strings.Repeat("x", 120), SocketName)
	if _, err := ListenUnix(long); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("long path err = %v", err)
	}
}
