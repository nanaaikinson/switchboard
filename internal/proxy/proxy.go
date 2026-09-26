package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// Proxy routes HTTP requests by Host header to local upstream ports.
// Implementations must be safe for concurrent use.
type Proxy interface {
	http.Handler
	// SetRoutes atomically replaces the route table. On error the old
	// table stays in place.
	SetRoutes(routes []config.Route) error
	// Routes returns a copy of the current route table.
	Routes() []config.Route
	// Lookup returns the route serving host, and whether it matched through
	// a wildcard rather than by exact name.
	Lookup(host string) (route config.Route, wildcard, ok bool)
	// Logs returns the recent requests to the route called name, oldest
	// first.
	Logs(name string) []AccessLog
}

// ReverseProxy is the standard-library Proxy built on httputil.ReverseProxy.
type ReverseProxy struct {
	table     atomic.Pointer[table]
	transport http.RoundTripper
	logs      accessLogs
	paused    atomic.Bool
}

var _ Proxy = (*ReverseProxy)(nil)

// New returns a ReverseProxy serving routes.
func New(routes []config.Route) (*ReverseProxy, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil // upstreams are always local; never use HTTP_PROXY
	p := &ReverseProxy{transport: t}
	if err := p.SetRoutes(routes); err != nil {
		return nil, err
	}
	return p, nil
}

// backend is one route with its prepared reverse proxy.
type backend struct {
	route config.Route
	rp    *httputil.ReverseProxy
}

// table is an immutable snapshot of the routes.
type table struct {
	routes   []config.Route
	exact    map[string]*backend // "myapp.test"
	wildcard map[string]*backend // base of "*.myapp.test" -> "myapp.test"
	suffixes map[string]bool     // last labels of all routes, e.g. "test"
}

// SetRoutes implements Proxy.
func (p *ReverseProxy) SetRoutes(routes []config.Route) error {
	if err := (&config.Config{Routes: routes}).Validate(); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	t := &table{
		routes:   slices.Clone(routes),
		exact:    map[string]*backend{},
		wildcard: map[string]*backend{},
		suffixes: map[string]bool{},
	}
	for _, r := range routes {
		b := &backend{route: r, rp: p.newReverseProxy(r.Port)}
		name := normalizeHost(r.Name)
		base, star := strings.CutPrefix(name, "*.")
		if !star {
			if t.exact[name] != nil {
				return fmt.Errorf("proxy: route %q: duplicate name", r.Name)
			}
			t.exact[name] = b
		}
		if star || r.Wildcard {
			if t.wildcard[base] != nil {
				return fmt.Errorf("proxy: route %q: wildcard *.%s already defined; remove one", r.Name, base)
			}
			t.wildcard[base] = b
		}
		t.suffixes[base[strings.LastIndexByte(base, '.')+1:]] = true
	}
	p.table.Store(t)
	names := make(map[string]bool, len(routes))
	for _, r := range routes {
		names[r.Name] = true
	}
	p.logs.keep(names)
	return nil
}

// SetPaused makes every route answer 503 while paused is true.
func (p *ReverseProxy) SetPaused(paused bool) { p.paused.Store(paused) }

// Logs implements Proxy.
func (p *ReverseProxy) Logs(name string) []AccessLog { return p.logs.get(name) }

// Routes implements Proxy.
func (p *ReverseProxy) Routes() []config.Route {
	return slices.Clone(p.table.Load().routes)
}

// Lookup implements Proxy.
func (p *ReverseProxy) Lookup(host string) (config.Route, bool, bool) {
	t := p.table.Load()
	host = normalizeHost(host)
	if b := t.exact[host]; b != nil {
		return b.route, false, true
	}
	if b := t.lookup(host); b != nil {
		return b.route, true, true
	}
	return config.Route{}, false, false
}

// ServeHTTP implements http.Handler.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := p.table.Load()
	host := normalizeHost(r.Host)
	if p.paused.Load() {
		pausedPage(w)
		return
	}
	if b := t.lookup(host); b != nil {
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		b.rp.ServeHTTP(rec, r)
		if rec.status == 0 && r.Header.Get("Upgrade") != "" {
			rec.status = http.StatusSwitchingProtocols // hijacked, so never seen here
		}
		p.logs.add(b.route.Name, AccessLog{
			Time: start, Host: host, Method: r.Method, Path: r.URL.Path, Status: rec.status,
			DurationMs: float64(time.Since(start).Microseconds()) / 1000,
		})
		return
	}
	notFound(w, host, t)
}

// lookup returns the exact match for host, else the longest wildcard match.
func (t *table) lookup(host string) *backend {
	if b := t.exact[host]; b != nil {
		return b
	}
	for rest := host; ; {
		i := strings.IndexByte(rest, '.')
		if i < 0 {
			return nil
		}
		rest = rest[i+1:]
		if b := t.wildcard[rest]; b != nil {
			return b
		}
	}
}

func (p *ReverseProxy) newReverseProxy(port int) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host // dev servers build URLs from Host
			// ReverseProxy drops the client's Forwarded and X-Forwarded-*
			// before Rewrite, but not X-Real-IP, through which a client could
			// claim another address. Both are deleted here so that doesn't
			// depend on the Go version.
			pr.Out.Header.Del("Forwarded")
			pr.Out.Header.Del("X-Real-IP")
			pr.SetXForwarded() // incoming X-Forwarded-* are dropped first
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return // client went away
			}
			slog.Debug("proxy upstream error", "port", port, "err", err)
			var opErr *net.OpError
			badGateway(w, port, errors.As(err, &opErr) && opErr.Op == "dial")
		},
	}
}

// normalizeHost lowercases host and strips any port and trailing dot.
func normalizeHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
