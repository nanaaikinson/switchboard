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
	s := newServiceWith(t, func(o *Options) { o.Reserved = []string{"switchboard.test"} })
	if _, _, err := s.Put(config.Route{Name: "switchboard", Port: 1}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("Put: %v", err)
	}
	if _, _, err := s.Put(config.Route{Name: "api.switchboard", Port: 1}); err != nil {
		t.Errorf("a subdomain is fine: %v", err)
	}
	res, err := s.Apply(ApplyRequest{File: "/p/switchboard.toml", Routes: []config.Route{{Name: "switchboard", Port: 2}}})
	if err != nil || len(res.Conflicts) != 1 || res.Conflicts[0].Owner != "the Switchboard dashboard" {
		t.Errorf("Apply: %+v, %v", res, err)
	}
	s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr("switchboard.test", 3, "switchboard")})
	if sk := s.Status().Docker.Skipped; len(sk) != 1 || !strings.Contains(sk[0].Reason, "the Switchboard dashboard") {
		t.Errorf("docker skipped = %+v", sk)
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
