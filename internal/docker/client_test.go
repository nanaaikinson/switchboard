package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeEngine serves the Engine API endpoints Switchboard uses on a Unix socket.
func fakeEngine(t *testing.T, path string, events ...string) (seen *[]string) {
	t.Helper()
	skipOnWindows(t)
	var paths []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("OK")) })
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`[{"Id":"abc","Names":["/web"],"Labels":{"a":"b"},"State":"running",
			"Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"}]}]`))
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		for _, e := range events {
			_, _ = fmt.Fprintln(w, e)
			w.(http.Flusher).Flush()
		}
	})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &paths
}

// shortDir is a temp dir short enough for Unix socket paths (104 bytes on macOS).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "sbd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func env(vars map[string]string, home string, probe ...string) Env {
	return Env{Getenv: func(k string) string { return vars[k] }, Home: home, Probe: probe}
}

func TestClientListsAndStreams(t *testing.T) {
	sock := filepath.Join(shortDir(t), "d.sock")
	seen := fakeEngine(t, sock, `{"Type":"container","Action":"start","Actor":{"ID":"abc"}}`, `{"Action":"die"}`)
	c := NewClient(Endpoint{Network: "unix", Addr: sock})
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	cs, err := c.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Name() != "web" || cs[0].Ports[0].PublicPort != 8080 || cs[0].Labels["a"] != "b" {
		t.Errorf("containers = %+v", cs)
	}

	events, errc := c.Events(ctx)
	var got []string
	for ev := range events {
		got = append(got, ev.Action+":"+ev.Actor.ID)
	}
	if strings.Join(got, ",") != "start:abc,die:" {
		t.Errorf("events = %v", got)
	}
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "event stream") {
		t.Errorf("stream end err = %v", err)
	}
	var filter map[string][]string
	q := (*seen)[1][strings.Index((*seen)[1], "filters=")+len("filters="):]
	if err := json.Unmarshal([]byte(unescape(t, q)), &filter); err != nil || !slices.Contains(filter["type"], "container") || !slices.Contains(filter["event"], "start") {
		t.Errorf("events filter %q: %v", q, err)
	}
	if (*seen)[0] != "/containers/json" {
		t.Errorf("containers path %q, want unversioned /containers/json", (*seen)[0])
	}
}

func unescape(t *testing.T, s string) string {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://x/?f="+s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r.URL.Query().Get("f")
}

// skipOnWindows skips tests of Unix-socket engines: Docker on Windows uses a
// named pipe, which Switchboard doesn't support yet.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Docker Engine over a Unix socket; Windows uses a named pipe")
	}
}

func TestClientReportsAPIErrors(t *testing.T) {
	skipOnWindows(t)
	sock := filepath.Join(shortDir(t), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"client version too old"}`, http.StatusBadRequest)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	_, err = NewClient(Endpoint{Network: "unix", Addr: sock}).Containers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "client version too old") {
		t.Errorf("err = %v", err)
	}
}

func TestFindRespectsDockerHost(t *testing.T) {
	dir := shortDir(t)
	custom := filepath.Join(dir, "custom.sock")
	probed := filepath.Join(dir, "probed.sock")
	fakeEngine(t, probed)
	// DOCKER_HOST wins even though a default socket works, and isn't pinged.
	ep, err := Find(context.Background(), env(map[string]string{"DOCKER_HOST": "unix://" + custom}, dir, probed))
	if err != nil || ep.String() != "unix://"+custom {
		t.Errorf("Find = %v, %v", ep, err)
	}
	ep, err = Find(context.Background(), env(map[string]string{"DOCKER_HOST": "tcp://127.0.0.1:2375"}, dir))
	if err != nil || ep.Network != "tcp" || ep.Addr != "127.0.0.1:2375" || ep.TLS != nil {
		t.Errorf("tcp: %+v, %v", ep, err)
	}
	for _, host := range []string{"ssh://me@box", "npipe:////./pipe/docker_engine", "::bad"} {
		if _, err := Find(context.Background(), env(map[string]string{"DOCKER_HOST": host}, dir)); err == nil {
			t.Errorf("DOCKER_HOST=%s accepted", host)
		}
	}
	// TLS without certs is an error, not a silent plaintext connection.
	if _, err := Find(context.Background(), env(map[string]string{"DOCKER_HOST": "tcp://127.0.0.1:2376", "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": dir}, dir)); err == nil {
		t.Error("DOCKER_TLS_VERIFY without certs accepted")
	}
}

func TestFindProbesSockets(t *testing.T) {
	skipOnWindows(t)
	dir := shortDir(t)
	stale := filepath.Join(dir, "stale.sock") // a socket file nobody listens on
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	notSocket := filepath.Join(dir, "file.sock")
	if err := os.WriteFile(notSocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.sock")
	fakeEngine(t, good)

	ep, err := Find(context.Background(), env(nil, dir, filepath.Join(dir, "missing.sock"), notSocket, stale, good))
	if err != nil || ep.Addr != good {
		t.Errorf("Find = %v, %v; want %s", ep, err, good)
	}
	_, err = Find(context.Background(), env(nil, dir, stale))
	if err == nil || !strings.Contains(err.Error(), "no Docker socket answered") || !strings.Contains(err.Error(), stale) {
		t.Errorf("stale only: %v", err)
	}
	_, err = Find(context.Background(), env(nil, dir, filepath.Join(dir, "missing.sock")))
	if err == nil || !strings.Contains(err.Error(), "no container engine running") {
		t.Errorf("none: %v", err)
	}
}

func TestDefaultSockets(t *testing.T) {
	socks := env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, "/home/me").Sockets()
	// Paths joined onto $HOME or $XDG_RUNTIME_DIR use the OS separator.
	for _, want := range []string{
		"/var/run/docker.sock", filepath.FromSlash("/home/me/.orbstack/run/docker.sock"),
		filepath.FromSlash("/home/me/.colima/default/docker.sock"), filepath.FromSlash("/home/me/.docker/run/docker.sock"),
		filepath.FromSlash("/run/user/1000/podman/podman.sock"), "/run/podman/podman.sock",
	} {
		if !slices.Contains(socks, want) {
			t.Errorf("Sockets() missing %s: %v", want, socks)
		}
	}
	if socks[0] != "/var/run/docker.sock" {
		t.Errorf("the standard socket should be tried first: %v", socks)
	}
}
