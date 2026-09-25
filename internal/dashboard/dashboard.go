// Package dashboard serves the web dashboard at https://switchboard.<tld>: the
// UI's static files and a limited view of the control API, behind a session
// cookie that only 'sb dashboard' can obtain.
package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io/fs"
	"net"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CookieName is the session cookie.
const CookieName = "sb_session"

// LoginTTL is how long a login link from NewLogin works. Each works once.
const LoginTTL = 2 * time.Minute

// Host is the dashboard's name under tld.
func Host(tld string) string { return "switchboard." + tld }

// csp allows only the dashboard's own files. Inline style attributes are
// allowed for the UI components' positioning; inline scripts are not.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// apiRoutes are the control API endpoints the dashboard may use. Everything
// else, such as POST /v1/apply, stays reachable only on the Unix socket.
var apiRoutes = []string{
	"GET /v1/routes", "POST /v1/routes", "DELETE /v1/routes/{name}", "GET /v1/routes/{name}/logs",
	"GET /v1/status", "GET /v1/events", "GET /v1/ca",
}

// Server serves the dashboard. It is safe for concurrent use.
type Server struct {
	api     http.Handler
	assets  fs.FS
	session string // the cookie value; new for every daemon run
	now     func() time.Time

	mu     sync.Mutex
	logins map[string]time.Time // one-time login token -> expiry
}

// New returns a dashboard for the control API handler api, serving the
// built UI in assets (index.html at the root).
func New(api http.Handler, assets fs.FS) *Server {
	mux := http.NewServeMux()
	for _, p := range apiRoutes {
		mux.Handle(p, api)
	}
	return &Server{api: mux, assets: assets, session: randomToken(), now: time.Now, logins: map[string]time.Time{}}
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails
	return hex.EncodeToString(b)
}

// NewLogin returns a one-time token for /login?token=..., valid for LoginTTL.
func (s *Server) NewLogin() string {
	tok := randomToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for t, exp := range s.logins {
		if now.After(exp) {
			delete(s.logins, t)
		}
	}
	s.logins[tok] = now.Add(LoginTTL)
	return tok
}

// LoginHandler serves POST /v1/dashboard/login on the control socket, which
// only the user can reach: {"token": "..."}.
func (s *Server) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": s.NewLogin()})
	})
}

// Wrap sends requests for the dashboard hosts to s, and everything else to
// next. Plain-HTTP requests for the dashboard are redirected to HTTPS on
// httpsPort; the dashboard is never served without TLS.
func (s *Server) Wrap(next http.Handler, hosts []string, httpsPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if !slices.Contains(hosts, host) {
			next.ServeHTTP(w, r)
			return
		}
		if r.TLS == nil {
			if httpsPort == 0 {
				page(w, http.StatusServiceUnavailable, "HTTPS isn't running", "The dashboard needs HTTPS. Run <code>sb doctor</code> to see why it's down.")
				return
			}
			if httpsPort != 443 {
				host = net.JoinHostPort(host, strconv.Itoa(httpsPort))
			}
			http.Redirect(w, r, "https://"+host+"/", http.StatusTemporaryRedirect) //nolint:gosec // G710: host is one of the dashboard's own names, matched above
			return
		}
		s.ServeHTTP(w, r)
	})
}

// ServeHTTP serves the dashboard over TLS.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	switch {
	case r.URL.Path == "/login":
		s.login(w, r)
	case !s.signedIn(r):
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			jsonError(w, http.StatusUnauthorized, "not signed in; open the dashboard with 'sb dashboard'")
			return
		}
		page(w, http.StatusUnauthorized, "Sign in from your terminal", "Open the dashboard with <code>sb dashboard</code>.")
	case strings.HasPrefix(r.URL.Path, "/v1/"):
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			jsonError(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		h.Set("Cache-Control", "no-store")
		s.api.ServeHTTP(w, r)
	default:
		s.static(w, r)
	}
}

// login trades a one-time token for the session cookie.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	s.mu.Lock()
	exp, ok := s.logins[tok]
	delete(s.logins, tok)
	s.mu.Unlock()
	if tok == "" || !ok || s.now().After(exp) {
		page(w, http.StatusForbidden, "This sign-in link has expired", "Links work once, for two minutes. Run <code>sb dashboard</code> again.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: s.session, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) signedIn(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.session)) == 1
}

// sameOrigin requires writes to come from the dashboard's own page: its
// Origin, and a header that a cross-site form can't send without a CORS
// preflight, which is never answered.
func sameOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == "https://"+r.Host && r.Header.Get("X-Requested-With") == "switchboard"
}

// static serves the UI. Unknown paths outside /assets get index.html, so
// client-side routes such as /settings and /routes/myapp.test (which looks
// like a file name) load the app.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	switch fi, err := fs.Stat(s.assets, name); {
	case errors.Is(err, fs.ErrNotExist) && strings.HasPrefix(name, "assets/"):
		http.NotFound(w, r) // a missing build file, not a page
		return
	case err != nil || fi.IsDir(): // never list directories
		name = "index.html"
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // file names carry a content hash
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, s.assets, name)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// page writes a minimal HTML message. body is trusted markup.
func page(w http.ResponseWriter, code int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta name="color-scheme" content="light dark">` +
		`<title>Switchboard</title><body style="font:16px system-ui;max-width:32rem;margin:15vh auto;padding:0 1rem">` +
		`<h1 style="font-size:1.25rem">` + html.EscapeString(title) + `</h1><p>` + body + `</p></body>`))
}

// hostOnly lowercases host and strips any port and trailing dot.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
