package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/proxy"
)

func newServiceWith(t *testing.T, edit func(*Options)) *Service {
	t.Helper()
	o := Options{
		ConfigPath: filepath.Join(t.TempDir(), config.FileName), Config: config.New(),
		Proxy: &fakeProxy{}, TLDs: []string{"test"}, HealthInterval: time.Hour,
	}
	edit(&o)
	s, err := NewService(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReservedNames(t *testing.T) {
	tests := []struct {
		name     string
		wildcard bool
		reserved bool
	}{
		{"switchboard", false, true},
		{"switchboard.test", false, true},
		{"api.switchboard", false, true},
		{"a.b.switchboard", false, true},
		{"*.switchboard", false, true},
		{"switchboard", true, true},
		{"myswitchboard", false, false},
		{"switchboard.myapp", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServiceWith(t, func(o *Options) { o.Reserved = []string{"switchboard.test"} })
			_, _, err := s.Put(config.Route{Name: tt.name, Port: 1, Wildcard: tt.wildcard})
			if got := errors.Is(err, ErrInvalid) && strings.Contains(err.Error(), "reserved"); got != tt.reserved || (!tt.reserved && err != nil) {
				t.Errorf("Put: %v, want reserved %v", err, tt.reserved)
			}

			s = newServiceWith(t, func(o *Options) { o.Reserved = []string{"switchboard.test"} })
			res, err := s.Apply(ApplyRequest{File: absPath("/p/switchboard.toml"), Routes: []config.Route{{Name: tt.name, Port: 2, Wildcard: tt.wildcard}}})
			if got := len(res.Conflicts) == 1 && res.Conflicts[0].Owner == "the Switchboard dashboard"; err != nil || got != tt.reserved {
				t.Errorf("Apply: %+v, %v, want reserved %v", res, err, tt.reserved)
			}

			s = newServiceWith(t, func(o *Options) { o.Reserved = []string{"switchboard.test"} })
			s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{{
				Route: config.Route{Name: s.qualify(tt.name), Port: 3, Wildcard: tt.wildcard}, Container: "c",
			}})
			sk := s.Status().Docker.Skipped
			if got := len(sk) == 1 && strings.Contains(sk[0].Reason, "the Switchboard dashboard"); got != tt.reserved {
				t.Errorf("docker skipped = %+v, want reserved %v", sk, tt.reserved)
			}
		})
	}
}

// Older versions only reserved switchboard.<tld> itself. Routes under it in an
// old routes.toml still load and stay saved, but are never served.
func TestReservedRoutesFromOldConfigAreKeptNotServed(t *testing.T) {
	fp := &fakeProxy{}
	s := newServiceWith(t, func(o *Options) {
		o.Reserved = []string{"switchboard.test"}
		o.Proxy = fp
		o.Config.Routes = []config.Route{{Name: "api.switchboard.test", Port: 1}, {Name: "web.test", Port: 2}}
	})
	if got := routeNames(fp.get()); got != "web.test" {
		t.Errorf("served = %s, want only web.test", got)
	}
	if _, _, err := s.Put(config.Route{Name: "other", Port: 3}); err != nil {
		t.Fatal(err)
	}
	if got := routeNames(fp.get()); got != "web.test,other.test" {
		t.Errorf("served after put = %s", got)
	}
	if got := routeNames(loadRoutes(t, s.opts.ConfigPath)); got != "api.switchboard.test,web.test,other.test" {
		t.Errorf("saved = %s, want the old route kept", got)
	}
	if _, err := s.Delete("api.switchboard"); err != nil {
		t.Errorf("the old route can still be removed: %v", err)
	}
}

func TestLogsAndCAEndpoints(t *testing.T) {
	logs := []proxy.AccessLog{{Method: "GET", Path: "/", Status: 200}}
	s := newServiceWith(t, func(o *Options) {
		o.Config.Routes = []config.Route{{Name: "web.test", Port: 1}}
		o.Logs = func(name string) []proxy.AccessLog {
			if name == "web.test" {
				return logs
			}
			return nil
		}
		o.CA = func() CAInfo { return CAInfo{Present: true, Fingerprint: "ab", Trusted: true} }
	})
	h := Handler(s)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get("/v1/routes/web/logs")
	var got []proxy.AccessLog
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got) != 1 || got[0].Path != "/" {
		t.Errorf("logs: %d %s", rec.Code, rec.Body)
	}
	if rec := get("/v1/routes/nope/logs"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown route logs: %d", rec.Code)
	}
	var ca CAInfo
	if rec := get("/v1/ca"); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &ca) != nil || !ca.Trusted || ca.Fingerprint != "ab" {
		t.Errorf("ca: %d %s", rec.Code, rec.Body)
	}
	// Without the hooks the endpoints still answer.
	bare := newServiceWith(t, func(o *Options) { o.Config.Routes = []config.Route{{Name: "web.test", Port: 1}} })
	if l, err := bare.Logs("web"); err != nil || l == nil || len(l) != 0 {
		t.Errorf("bare logs %#v, %v", l, err)
	}
	if bare.CA().Present {
		t.Error("bare CA present")
	}
}
