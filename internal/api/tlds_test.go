package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/mdns"
)

// fakeAnnouncer records the latest Update and "announces" every name.
type fakeAnnouncer struct {
	mu      sync.Mutex
	enabled bool
	names   []string
	err     string
}

func (f *fakeAnnouncer) Update(enabled bool, names []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled, f.names = enabled, slices.Clone(names)
}

func (f *fakeAnnouncer) State() mdns.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.enabled {
		return mdns.State{}
	}
	st := mdns.State{Backend: "fake", Interface: "loopback", Error: f.err}
	if f.err == "" {
		st.Announced = slices.Clone(f.names)
	}
	return st
}

func (f *fakeAnnouncer) get() (bool, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled, slices.Clone(f.names)
}

func newMDNSService(t *testing.T, cfg *config.Config) (*Service, *fakeAnnouncer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), config.FileName)
	fa := &fakeAnnouncer{}
	s, err := NewService(Options{
		ConfigPath: path, Config: cfg, Proxy: &fakeProxy{}, TLDs: []string{"test"},
		HealthInterval: time.Hour, MDNS: fa,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s, fa, path
}

var localTLD = config.TLD{Name: "local", MDNS: true}

func TestAddTLD(t *testing.T) {
	tests := []struct {
		name        string
		tld         string
		mdns        bool
		noAnnouncer bool
		wantCreated bool
		wantErr     error
		wantMsg     string
	}{
		{name: "local over mdns", tld: "local", mdns: true, wantCreated: true},
		{name: "normalized", tld: " .LOCAL. ", mdns: true, wantCreated: true},
		{name: "local without mdns", tld: "local", wantErr: ErrInvalid, wantMsg: "sb tld add local --mdns"},
		{name: "mdns on other tld", tld: "lan", mdns: true, wantErr: ErrInvalid, wantMsg: "only works for .local"},
		{name: "unicast tld", tld: "dev", wantErr: ErrInvalid, wantMsg: "only .local"},
		{name: "default tld", tld: "test", wantCreated: false},
		{name: "default tld over mdns", tld: "test", mdns: true, wantErr: ErrInvalid, wantMsg: "split DNS"},
		{name: "no announcer", tld: "local", mdns: true, noAnnouncer: true, wantErr: ErrInvalid, wantMsg: "can't announce"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, fa, path := newMDNSService(t, config.New())
			if tt.noAnnouncer {
				s.opts.MDNS = nil
			}
			created, err := s.AddTLD(tt.tld, tt.mdns)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) || !strings.Contains(err.Error(), tt.wantMsg) {
					t.Fatalf("err = %v, want %v containing %q", err, tt.wantErr, tt.wantMsg)
				}
				return
			}
			if err != nil || created != tt.wantCreated {
				t.Fatalf("AddTLD = %v, %v; want %v, nil", created, err, tt.wantCreated)
			}
			if !created {
				return
			}
			cfg, err := config.Load(path)
			if err != nil || !slices.Equal(cfg.TLDs, []config.TLD{localTLD}) {
				t.Errorf("saved TLDs = %v (%v), want [local mdns]", cfg.TLDs, err)
			}
			if enabled, _ := fa.get(); !enabled {
				t.Error("announcer not enabled")
			}
			if again, err := s.AddTLD("local", true); again || err != nil {
				t.Errorf("second AddTLD = %v, %v; want false, nil", again, err)
			}
			want := []TLD{{Name: "test", Default: true}, {Name: "local", MDNS: true}}
			if got := s.TLDs(); !slices.Equal(got, want) {
				t.Errorf("TLDs() = %v, want %v", got, want)
			}
		})
	}
}

func TestAnnouncesExactNamesUnderLocal(t *testing.T) {
	s, fa, _ := newMDNSService(t, &config.Config{SchemaVersion: 1, TLDs: []config.TLD{localTLD}, Routes: []config.Route{
		{Name: "myapp.test", Port: 1},
		{Name: "myapp.local", Port: 2, Wildcard: true},
	}})
	if enabled, names := fa.get(); !enabled || !slices.Equal(names, []string{"myapp.local"}) {
		t.Fatalf("at start: enabled %v, names %v; want true, [myapp.local]", enabled, names)
	}

	for _, r := range []config.Route{{Name: "api.myapp.local", Port: 3}, {Name: "*.tenants.local", Port: 4}} {
		if _, _, err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	s.SetDocker(DockerStatus{Enabled: true}, []DockerRoute{{Route: config.Route{Name: "db.local", Port: 5}, Container: "db"}})
	_, names := fa.get()
	if want := []string{"myapp.local", "api.myapp.local", "db.local"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}

	got := map[string]string{}
	for _, r := range s.Routes() {
		got[r.Name] = r.MDNS
	}
	want := map[string]string{
		"myapp.test": "", "myapp.local": MDNSAnnounced, "api.myapp.local": MDNSAnnounced,
		"*.tenants.local": MDNSWildcard, "db.local": MDNSAnnounced,
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("route %s mdns = %q, want %q", n, got[n], w)
		}
	}

	if _, err := s.Delete("api.myapp.local"); err != nil {
		t.Fatal(err)
	}
	if _, names := fa.get(); slices.Contains(names, "api.myapp.local") {
		t.Errorf("removed route still announced: %v", names)
	}

	st := s.Status()
	if !st.MDNS.Enabled || !st.MDNS.Experimental || st.MDNS.Backend != "fake" || st.MDNS.Announced != 2 ||
		!slices.Equal(st.MDNS.TLDs, []string{"local"}) || !slices.Equal(st.TLDs, []string{"test", "local"}) {
		t.Errorf("status = tlds %v, mdns %+v", st.TLDs, st.MDNS)
	}
}

func TestRouteMDNSPendingOnError(t *testing.T) {
	s, fa, _ := newMDNSService(t, &config.Config{SchemaVersion: 1, TLDs: []config.TLD{localTLD},
		Routes: []config.Route{{Name: "myapp.local", Port: 1}}})
	fa.mu.Lock()
	fa.err = "no responder"
	fa.mu.Unlock()
	if rs := s.Routes(); rs[0].MDNS != MDNSPending {
		t.Errorf("mdns = %q, want pending", rs[0].MDNS)
	}
	if st := s.Status(); st.MDNS.Error != "no responder" {
		t.Errorf("status error = %q", st.MDNS.Error)
	}
}

func TestRemoveTLD(t *testing.T) {
	s, fa, path := newMDNSService(t, &config.Config{SchemaVersion: 1, TLDs: []config.TLD{localTLD},
		Routes: []config.Route{{Name: "myapp.local", Port: 1}, {Name: "myapp.test", Port: 2}}})

	if err := s.RemoveTLD("test"); !errors.Is(err, ErrInvalid) {
		t.Errorf("remove default: err = %v, want ErrInvalid", err)
	}
	if err := s.RemoveTLD("lan"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove unknown: err = %v, want ErrNotFound", err)
	}
	err := s.RemoveTLD("local")
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "sb rm myapp.local") {
		t.Fatalf("remove in use: err = %v, want ErrConflict naming myapp.local", err)
	}

	if _, err := s.Delete("myapp.local"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTLD("local"); err != nil {
		t.Fatalf("RemoveTLD: %v", err)
	}
	if enabled, _ := fa.get(); enabled {
		t.Error("announcer still enabled")
	}
	cfg, err := config.Load(path)
	if err != nil || len(cfg.TLDs) != 0 || len(cfg.Routes) != 1 {
		t.Errorf("saved config = %+v (%v), want no TLDs and myapp.test", cfg, err)
	}
	if got := s.qualify("myapp.local"); got != "myapp.local.test" {
		t.Errorf("after removal myapp.local qualifies to %s", got)
	}
}

func TestRouteChangesKeepTLDs(t *testing.T) {
	s, _, path := newMDNSService(t, &config.Config{SchemaVersion: 1, TLDs: []config.TLD{localTLD}})
	if _, _, err := s.Put(config.Route{Name: "myapp", Port: 1}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil || !slices.Equal(cfg.TLDs, []config.TLD{localTLD}) {
		t.Errorf("TLDs after Put = %v (%v)", cfg.TLDs, err)
	}
}

func TestTLDHandlers(t *testing.T) {
	s, _, _ := newMDNSService(t, config.New())
	srv := httptest.NewServer(Handler(s))
	defer srv.Close()
	do := func(method, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	steps := []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/v1/tlds", "", http.StatusOK},
		{"PUT", "/v1/tlds/local", `{"mdns":true}`, http.StatusCreated},
		{"PUT", "/v1/tlds/local", `{"mdns":true}`, http.StatusOK},
		{"PUT", "/v1/tlds/local", `{"mdns":false}`, http.StatusBadRequest},
		{"PUT", "/v1/tlds/local", `{"lan":true}`, http.StatusBadRequest},
		{"DELETE", "/v1/tlds/local", "", http.StatusOK},
		{"DELETE", "/v1/tlds/local", "", http.StatusNotFound},
	}
	for _, st := range steps {
		if got := do(st.method, st.path, st.body); got != st.want {
			t.Errorf("%s %s %s = %d, want %d", st.method, st.path, st.body, got, st.want)
		}
	}
}

func TestLocalNamesNeedLocalMode(t *testing.T) {
	s, _, _ := newMDNSService(t, config.New())
	for _, name := range []string{"myapp.local", "api.myapp.LOCAL."} {
		if _, _, err := s.Put(config.Route{Name: name, Port: 1}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "sb tld add local --mdns") {
			t.Errorf("Put(%s) err = %v, want hint to enable .local", name, err)
		}
	}
	if _, err := s.Apply(ApplyRequest{File: filepath.Join(t.TempDir(), "switchboard.toml"), Routes: []config.Route{{Name: "x.local", Port: 1}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Apply err = %v, want ErrInvalid", err)
	}
	if _, err := s.AddTLD("local", true); err != nil {
		t.Fatal(err)
	}
	if rs, _, err := s.Put(config.Route{Name: "myapp.local", Port: 1}); err != nil || rs.Name != "myapp.local" {
		t.Errorf("Put after enabling = %v, %v", rs.Name, err)
	}
}
