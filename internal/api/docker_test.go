package api

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

func dr(name string, port int, container string) DockerRoute {
	return DockerRoute{Route: config.Route{Name: name, Port: port, RedirectHTTPS: true}, Container: container}
}

func routeNames(rs []config.Route) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return strings.Join(out, ",")
}

func nextEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func TestDockerRoutesServedNotPersisted(t *testing.T) {
	s, fp, path := newTestService(t, config.Route{Name: "myapp.test", Port: 7000})
	events, cancel := s.hub.subscribe()
	defer cancel()

	s.SetDocker(DockerStatus{Enabled: true, Connected: true, Endpoint: "unix:///x.sock"},
		[]DockerRoute{dr("web.test", 8080, "web"), dr("api.shop.test", 3000, "shop-api-1")})
	if got := routeNames(fp.get()); got != "myapp.test,web.test,api.shop.test" {
		t.Errorf("proxy routes = %s", got)
	}
	if got := routeNames(loadRoutes(t, path)); got != "" {
		t.Errorf("Docker routes were saved: %s", got)
	}
	for _, want := range []string{"web.test", "api.shop.test"} {
		e := nextEvent(t, events)
		if e.Type != EventRouteAdded || e.Route.Name != want || e.Route.Source != SourceDocker {
			t.Errorf("event %+v, want route.added %s from docker", e, want)
		}
	}

	rs := s.Routes()
	if len(rs) != 3 || rs[0].Source != SourceConfig || rs[1].Source != SourceDocker || rs[1].Container != "web" {
		t.Errorf("Routes() = %+v", rs)
	}
	st := s.Status()
	if !st.Docker.Connected || st.Docker.Endpoint != "unix:///x.sock" {
		t.Errorf("status docker = %+v", st.Docker)
	}

	// A container stops: its route goes, with an event.
	s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr("web.test", 8080, "web")})
	if e := nextEvent(t, events); e.Type != EventRouteRemoved || e.Route.Name != "api.shop.test" {
		t.Errorf("event %+v, want route.removed api.shop.test", e)
	}
	// Its port changes: updated.
	s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr("web.test", 9090, "web")})
	if e := nextEvent(t, events); e.Type != EventRouteUpdated || e.Route.Port != 9090 {
		t.Errorf("event %+v, want route.updated to 9090", e)
	}
	// Docker goes away: every Docker route is dropped.
	s.SetDocker(DockerStatus{Enabled: true, Error: "gone"}, nil)
	if got := routeNames(fp.get()); got != "myapp.test" {
		t.Errorf("after disconnect proxy routes = %s", got)
	}
}

func TestConfigRouteWinsOverDocker(t *testing.T) {
	s, fp, _ := newTestService(t, config.Route{Name: "myapp.test", Port: 7000, Wildcard: true})
	s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{
		dr("myapp.test", 8080, "clash"),
		dr("*.myapp.test", 8081, "clash-wild"), // the config route is a wildcard too
		dr("web.test", 8082, "web"),
		dr("*.web.test", 8083, "web-wild"),
	})
	if got := routeNames(fp.get()); got != "myapp.test,web.test,*.web.test" {
		t.Errorf("proxy routes = %s", got)
	}
	sk := s.Status().Docker.Skipped
	if len(sk) != 2 || sk[0].Container != "clash" || !strings.Contains(sk[0].Reason, "taken by a route in routes.toml") {
		t.Errorf("skipped = %+v", sk)
	}

	// sb add over a Docker name: the config route takes it, the container's
	// route is displaced and reported.
	if _, _, err := s.Put(config.Route{Name: "web", Port: 7001}); err != nil {
		t.Fatal(err)
	}
	if got := routeNames(fp.get()); got != "myapp.test,web.test,*.web.test" {
		t.Errorf("after put proxy routes = %s", got)
	}
	for _, r := range s.Routes() {
		if r.Name == "web.test" && (r.Source != SourceConfig || r.Port != 7001) {
			t.Errorf("web.test = %+v, want the config route", r)
		}
	}
	if sk := s.Status().Docker.Skipped; len(sk) != 3 || sk[2].Container != "web" {
		t.Errorf("skipped after put = %+v", sk)
	}
	// Removing the config route gives the name back to the container.
	if _, err := s.Delete("web"); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.Routes() {
		if r.Name == "web.test" && (r.Source != SourceDocker || r.Port != 8082) {
			t.Errorf("web.test = %+v, want the Docker route back", r)
		}
	}
}

// The proxy prefers exact names over wildcards, so a container name under a
// config or project-file wildcard would take those names from it.
func TestDockerCantShadowConfigWildcard(t *testing.T) {
	cfg := []config.Route{
		{Name: "*.myapp.test", Port: 7000},
		{Name: "shop.test", Port: 7001, Wildcard: true, File: "/p/switchboard.toml"},
		{Name: "exact.test", Port: 7002},
	}
	tests := []struct {
		docker string
		served bool
	}{
		{"admin.myapp.test", false},
		{"a.b.myapp.test", false},
		{"*.api.myapp.test", false},
		{"myapp.test", false},
		{"admin.shop.test", false},
		{"*.shop.test", false},
		{"shop.test", false},
		{"api.exact.test", true}, // exact.test has no wildcard
		{"*.exact.test", true},
		{"notmyapp.test", true},
	}
	for _, tt := range tests {
		t.Run(tt.docker, func(t *testing.T) {
			s, fp, _ := newTestService(t, cfg...)
			s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr(tt.docker, 9000, "c")})
			served := slices.ContainsFunc(fp.get(), func(r config.Route) bool { return r.Port == 9000 })
			sk := s.Status().Docker.Skipped
			if served != tt.served || (tt.served && len(sk) != 0) {
				t.Fatalf("served %v, want %v; skipped %+v", served, tt.served, sk)
			}
			if !tt.served && (len(sk) != 1 || sk[0].Container != "c" || !strings.Contains(sk[0].Reason, "dev.switchboard.hosts")) {
				t.Errorf("skipped = %+v, want a reason that says what to change", sk)
			}
		})
	}

	// A wildcard added later displaces a container already served under it.
	s, fp, _ := newTestService(t)
	s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr("admin.later.test", 9000, "c")})
	if _, _, err := s.Put(config.Route{Name: "*.later", Port: 7003}); err != nil {
		t.Fatal(err)
	}
	if got := routeNames(fp.get()); got != "*.later.test" {
		t.Errorf("served = %s, want only the wildcard", got)
	}
}

func TestDeleteDockerRouteIsConflict(t *testing.T) {
	s, _, _ := newTestService(t)
	s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr("web.test", 8080, "web")})
	_, err := s.Delete("web")
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "Docker container web") || !strings.Contains(err.Error(), "dev.switchboard.enable=false") {
		t.Errorf("err = %v", err)
	}
	rec := httptest.NewRecorder()
	Handler(s).ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/routes/web", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("DELETE status %d, want 409", rec.Code)
	}
}

// pickyProxy rejects any route table with a route it refuses, as the proxy
// rejects an invalid table.
type pickyProxy struct {
	fakeProxy
	refuse func(config.Route) bool
}

func (p *pickyProxy) SetRoutes(rs []config.Route) error {
	for _, r := range rs {
		if p.refuse != nil && p.refuse(r) {
			return fmt.Errorf("proxy: route %q: refused", r.Name)
		}
	}
	return p.fakeProxy.SetRoutes(rs)
}

// Hostnames never reach the log at info level or above (AGENTS.md); the full
// reason is in the skipped list instead.
func TestDockerRejectionLogsNoHostnames(t *testing.T) {
	tests := []struct {
		name   string
		refuse func(config.Route) bool
		want   []string // in the log
	}{
		{"docker routes rejected", func(r config.Route) bool { return r.Name == "secret-app.test" },
			[]string{"docker routes rejected"}},
		{"config routes can't be restored either", func(r config.Route) bool { return r.Port == 9000 },
			[]string{"docker routes rejected", "restore config routes failed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &pickyProxy{}
			s := newServiceWith(t, func(o *Options) {
				o.Proxy = p
				o.Config.Routes = []config.Route{{Name: "private-config.test", Port: 9000}}
			})
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			t.Cleanup(func() { slog.SetDefault(old) })

			p.refuse = tt.refuse
			s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr("secret-app.test", 8080, "web")})
			out := buf.String()
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("log missing %q:\n%s", w, out)
				}
			}
			if strings.Contains(out, "secret-app") || strings.Contains(out, "private-config") {
				t.Errorf("hostname logged at info or above:\n%s", out)
			}
			if sk := s.Status().Docker.Skipped; len(sk) != 1 || !strings.Contains(sk[0].Reason, "refused") {
				t.Errorf("skipped = %+v, want the full reason there", sk)
			}
		})
	}
}

func TestDockerRoutesHealth(t *testing.T) {
	s, _, _ := newTestService(t)
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()
	go s.Run(t.Context())
	s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr("web.test", up.Listener.Addr().(*net.TCPAddr).Port, "web")})
	waitHealth(t, s, "web.test", HealthUp)
}
