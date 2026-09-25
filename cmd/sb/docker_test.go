package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/docker"
)

// fakeDocker is a Docker engine in memory for the daemon.
type fakeDocker struct {
	mu         sync.Mutex
	containers []docker.Container
	events     chan docker.Event
}

func (f *fakeDocker) Containers(context.Context) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]docker.Container(nil), f.containers...), nil
}

func (f *fakeDocker) Events(context.Context) (<-chan docker.Event, <-chan error) {
	return f.events, make(chan error)
}

func (f *fakeDocker) set(cs ...docker.Container) {
	f.mu.Lock()
	f.containers = cs
	f.mu.Unlock()
	f.events <- docker.Event{Action: "start"}
}

func published(host, container int) docker.Port {
	return docker.Port{IP: "0.0.0.0", PrivatePort: container, PublicPort: host, Type: "tcp"}
}

func waitRoutes(t *testing.T, cond func([]api.RouteStatus) bool) []api.RouteStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var rs []api.RouteStatus
		if err := json.Unmarshal([]byte(mustRun(t, "ls", "--json")), &rs); err != nil {
			t.Fatal(err)
		}
		if cond(rs) {
			return rs
		}
		if time.Now().After(deadline) {
			t.Fatalf("routes never matched: %+v", rs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func hasRoute(rs []api.RouteStatus, name string) bool {
	for _, r := range rs {
		if r.Name == name {
			return true
		}
	}
	return false
}

func TestDockerRoutesThroughTheDaemon(t *testing.T) {
	dir := configDir(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("from container")) }))
	defer up.Close()
	port := up.Listener.Addr().(*net.TCPAddr).Port

	fake := &fakeDocker{events: make(chan docker.Event, 8), containers: []docker.Container{
		{ID: "1", Names: []string{"/web"}, Ports: []docker.Port{published(port, 80)}},
		{ID: "2", Names: []string{"/shop-worker-1"}, Ports: []docker.Port{published(5001, 5000), published(6001, 6000)},
			Labels: map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "worker"}},
		{ID: "3", Names: []string{"/db"}, Ports: []docker.Port{{PrivatePort: 5432, Type: "tcp"}}},
	}}
	startDaemon(t, daemonOptions{dockerConnect: func(context.Context) (docker.Client, string, error) { return fake, "unix:///fake.sock", nil }})

	rs := waitRoutes(t, func(rs []api.RouteStatus) bool { return hasRoute(rs, "web.test") })
	if len(rs) != 1 || rs[0].Source != api.SourceDocker || rs[0].Container != "web" || rs[0].Port != port || !rs[0].RedirectHTTPS {
		t.Errorf("routes = %+v", rs)
	}
	ls := mustRun(t, "ls")
	for _, want := range []string{"SOURCE", "docker (web)", "Docker containers without a route:",
		"shop-worker-1: it publishes 2 ports (5001->5000, 6001->6000) and none is container port 80, 8080 or 3000; set dev.switchboard.port"} {
		if !strings.Contains(ls, want) {
			t.Errorf("ls missing %q:\n%s", want, ls)
		}
	}
	if strings.Contains(ls, "db") {
		t.Errorf("a container without published ports was listed:\n%s", ls)
	}

	// The proxy serves the container's route over HTTPS.
	st := status(t, dir)
	resp, err := httpsClient(t, dir, st.HTTPS.Addrs[0]).Get("https://web.test/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "from container" {
		t.Errorf("body = %q", body)
	}
	if st.Docker.Endpoint != "unix:///fake.sock" || !st.Docker.Connected {
		t.Errorf("docker status = %+v", st.Docker)
	}

	// Docker routes can't be removed with sb rm, and are never saved.
	if _, err := run(t, "rm", "web"); err == nil || !strings.Contains(err.Error(), "comes from Docker container web") {
		t.Errorf("rm web: %v", err)
	}
	mustRun(t, "add", "api", "7000")
	cfg, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Name != "api.test" {
		t.Errorf("saved routes = %+v", cfg.Routes)
	}

	// The container stops: its route goes.
	fake.set()
	waitRoutes(t, func(rs []api.RouteStatus) bool { return !hasRoute(rs, "web.test") && hasRoute(rs, "api.test") })
}
