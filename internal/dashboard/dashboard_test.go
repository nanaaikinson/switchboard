package dashboard

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var assets = fstest.MapFS{
	"index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
	"assets/app-abc.js":  {Data: []byte("console.log(1)")},
	"assets/app-abc.css": {Data: []byte("body{}")},
	"favicon.svg":        {Data: []byte("<svg/>")},
}

// fakeAPI records what reaches the control API.
type fakeAPI struct{ hits []string }

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.hits = append(f.hits, r.Method+" "+r.URL.Path)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

type env struct {
	s   *Server
	api *fakeAPI
	h   http.Handler
}

func newEnv() *env {
	api := &fakeAPI{}
	s := New(api, assets)
	return &env{s: s, api: api, h: s.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("proxied"))
	}), []string{"switchboard.test"}, 8443)}
}

// do sends a request as if over TLS (unless plain) with the given cookie and headers.
func (e *env) do(method, target, cookie string, plain bool, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "switchboard.test:8443"
	if !plain {
		req.TLS = &tls.ConnectionState{}
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// signIn follows a login link and returns the session cookie.
func (e *env) signIn(t *testing.T) string {
	t.Helper()
	rec := e.do("GET", "/login?token="+e.s.NewLogin(), "", false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	c := rec.Result().Cookies()
	// __Host- cookies must be Secure, Path=/ and have no Domain, or browsers drop them.
	if len(c) != 1 || c[0].Name != CookieName || !strings.HasPrefix(c[0].Name, "__Host-") || c[0].Domain != "" ||
		!c[0].HttpOnly || !c[0].Secure || c[0].SameSite != http.SameSiteStrictMode || c[0].Path != "/" {
		t.Fatalf("cookie %+v", c)
	}
	return c[0].Value
}

func TestLoginIsOneTimeAndExpires(t *testing.T) {
	e := newEnv()
	tok := e.s.NewLogin()
	if rec := e.do("GET", "/login?token="+tok, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("first use: %d", rec.Code)
	}
	if rec := e.do("GET", "/login?token="+tok, "", false); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "sb dashboard") {
		t.Errorf("reuse: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"", "nope"} {
		if rec := e.do("GET", "/login?token="+bad, "", false); rec.Code != http.StatusForbidden {
			t.Errorf("token %q: %d", bad, rec.Code)
		}
	}
	now := time.Now()
	e.s.now = func() time.Time { return now }
	old := e.s.NewLogin()
	e.s.now = func() time.Time { return now.Add(LoginTTL + time.Second) }
	if rec := e.do("GET", "/login?token="+old, "", false); rec.Code != http.StatusForbidden {
		t.Errorf("expired: %d", rec.Code)
	}
}

func TestEverythingNeedsTheSession(t *testing.T) {
	e := newEnv()
	for _, p := range []string{"/", "/settings", "/assets/app-abc.js", "/v1/status"} {
		for _, cookie := range []string{"", "forged"} {
			rec := e.do("GET", p, cookie, false)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("GET %s with cookie %q: %d", p, cookie, rec.Code)
			}
		}
	}
	if rec := e.do("GET", "/v1/status", "", false); !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("API 401 should be JSON: %s", rec.Body)
	}
	if len(e.api.hits) != 0 {
		t.Errorf("API reached without a session: %v", e.api.hits)
	}
	// A session from another daemon run (another Server) is not valid.
	other := newEnv().signIn(t)
	if rec := e.do("GET", "/", other, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("foreign session: %d", rec.Code)
	}
}

func TestAPIAllowlistAndCSRF(t *testing.T) {
	e := newEnv()
	c := e.signIn(t)
	ok := []string{"Origin", "https://switchboard.test:8443", "X-Requested-With", "switchboard"}
	for _, tc := range []struct {
		method, path string
		hdr          []string
		want         int
	}{
		{"GET", "/v1/routes", nil, 200},
		{"GET", "/v1/status", nil, 200},
		{"GET", "/v1/events", nil, 200},
		{"GET", "/v1/ca", nil, 200},
		{"GET", "/v1/routes/web.test/logs", nil, 200},
		{"POST", "/v1/routes", ok, 200},
		{"DELETE", "/v1/routes/web.test", ok, 200},
		{"POST", "/v1/apply", ok, 404},           // socket only
		{"POST", "/v1/dashboard/login", ok, 404}, // socket only
		{"POST", "/v1/routes", nil, 403},         // no Origin
		{"POST", "/v1/routes", []string{"Origin", "https://evil.test", "X-Requested-With", "switchboard"}, 403},
		{"DELETE", "/v1/routes/x", []string{"Origin", "https://switchboard.test:8443"}, 403}, // no custom header
	} {
		rec := e.do(tc.method, tc.path, c, false, tc.hdr...)
		if rec.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
	want := "GET /v1/routes,GET /v1/status,GET /v1/events,GET /v1/ca,GET /v1/routes/web.test/logs,POST /v1/routes,DELETE /v1/routes/web.test"
	if got := strings.Join(e.api.hits, ","); got != want {
		t.Errorf("API saw %s\nwant %s", got, want)
	}
}

func TestStaticFiles(t *testing.T) {
	e := newEnv()
	c := e.signIn(t)
	for _, tc := range []struct {
		path, body, cache string
		code              int
	}{
		{"/", "<div id=root>", "no-cache", 200},
		{"/settings", "<div id=root>", "no-cache", 200},              // client-side route
		{"/routes/api.myapp.test", "<div id=root>", "no-cache", 200}, // a route name looks like a file
		{"/assets", "<div id=root>", "no-cache", 200},                // a directory: never listed
		{"/assets/app-abc.js", "console.log", "immutable", 200},
		{"/assets/missing.js", "", "", 404},
		{"/../../etc/passwd", "invalid URL path", "", 400}, // refused by ServeFileFS
	} {
		rec := e.do("GET", tc.path, c, false)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.body) || !strings.Contains(rec.Header().Get("Cache-Control"), tc.cache) {
			t.Errorf("GET %s: %d %q cache %q", tc.path, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
	}
	rec := e.do("GET", "/", c, false)
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers %v", rec.Header())
	}
	if rec := e.do("POST", "/", c, false); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d", rec.Code)
	}
}

func TestWrap(t *testing.T) {
	e := newEnv()
	// Other hosts go to the proxy; names under the dashboard's never do.
	for _, tc := range []struct {
		host string
		code int
		body string
	}{
		{"myapp.test", 200, "proxied"},
		{"myswitchboard.test", 200, "proxied"},
		{"api.switchboard.test", 404, "reserved"},
		{"A.B.Switchboard.Test.:443", 404, "reserved"},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if body, _ := io.ReadAll(rec.Body); rec.Code != tc.code || !strings.Contains(string(body), tc.body) {
			t.Errorf("%s: %d %q", tc.host, rec.Code, body)
		}
	}
	// Plain HTTP to the dashboard goes to HTTPS, never serving anything.
	rec2 := e.do("GET", "/login?token="+e.s.NewLogin(), "", true)
	if rec2.Code != http.StatusTemporaryRedirect || rec2.Header().Get("Location") != "https://switchboard.test:8443/" || len(rec2.Result().Cookies()) != 0 {
		t.Errorf("plain HTTP: %d %s", rec2.Code, rec2.Header().Get("Location"))
	}
	noTLS := New(&fakeAPI{}, assets).Wrap(http.NotFoundHandler(), []string{"switchboard.test"}, 0)
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "Switchboard.Test."
	rec := httptest.NewRecorder()
	noTLS.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "sb doctor") {
		t.Errorf("without HTTPS: %d %s", rec.Code, rec.Body)
	}
}

func TestLoginHandler(t *testing.T) {
	e := newEnv()
	rec := httptest.NewRecorder()
	e.s.LoginHandler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/dashboard/login", nil))
	var tok string
	if b := rec.Body.String(); strings.Contains(b, `"token":"`) {
		tok = b[strings.Index(b, `"token":"`)+9 : strings.LastIndex(b, `"`)]
	}
	if len(tok) != 64 {
		t.Fatalf("token %q from %s", tok, rec.Body)
	}
	if rec := e.do("GET", "/login?token="+tok, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("handler token rejected: %d", rec.Code)
	}
}
