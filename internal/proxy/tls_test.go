package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/pki"
)

func TestRedirectHTTPS(t *testing.T) {
	_, port := upstream(t, "app")
	p, err := New([]config.Route{
		{Name: "myapp.test", Port: port, RedirectHTTPS: true, Wildcard: true},
		{Name: "plain.test", Port: port},
	})
	if err != nil {
		t.Fatal(err)
	}
	noFollow := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	tests := []struct {
		name, host, path string
		httpsPort        int
		wantCode         int
		wantLocation     string
	}{
		{"redirects", "myapp.test", "/a/b?q=1", 8443, 307, "https://myapp.test:8443/a/b?q=1"},
		{"default port omitted", "MyApp.test:80", "/", 443, 307, "https://myapp.test/"},
		{"wildcard subdomain", "api.myapp.test", "/x", 443, 307, "https://api.myapp.test/x"},
		{"redirect off", "plain.test", "/", 443, 200, ""},
		{"no route", "nope.test", "/", 443, 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(RedirectHTTPS(p, tt.httpsPort))
			defer srv.Close()
			req, _ := http.NewRequest(http.MethodGet, srv.URL+tt.path, nil)
			req.Host = tt.host
			c := srv.Client()
			c.CheckRedirect = noFollow
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.wantCode || resp.Header.Get("Location") != tt.wantLocation {
				t.Errorf("got %d %q, want %d %q", resp.StatusCode, resp.Header.Get("Location"), tt.wantCode, tt.wantLocation)
			}
		})
	}
}

// A wildcard route matches any first label. Go's server already rejects Host
// headers with "/", "@" or "\\", but accepts other non-hostname characters;
// only real hostnames may be echoed into a Location header.
func TestRedirectRejectsHostileHosts(t *testing.T) {
	_, port := upstream(t, "app")
	p, err := New([]config.Route{{Name: "*.myapp.test", Port: port, RedirectHTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(RedirectHTTPS(p, 443))
	defer srv.Close()
	for _, host := range []string{"a!b.myapp.test", "(x).myapp.test", "a_b.myapp.test", "a%2fb.myapp.test"} {
		c, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		resp.Body.Close()
		c.Close()
		if loc := resp.Header.Get("Location"); loc != "" {
			t.Errorf("Host %q redirected to %q", host, loc)
		}
	}
}

func TestServeTLSWithHTTP2(t *testing.T) {
	ca, err := pki.LoadOrCreate(filepath.Join(t.TempDir(), "pki"), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	_, port := upstream(t, "app")
	p, err := New([]config.Route{{Name: "myapp.test", Port: port, RedirectHTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// The redirect wrapper must pass TLS requests through untouched.
	go func() {
		done <- ServeTLS(ctx, RedirectHTTPS(p, 443), []net.Listener{ln}, pki.NewIssuer(ca, pki.IssuerOptions{}).TLSConfig())
	}()

	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		},
	}}
	resp, err := client.Get("https://myapp.test/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	var echo map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&echo)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Upstream") != "app" {
		t.Errorf("status %d, upstream %q", resp.StatusCode, resp.Header.Get("X-Upstream"))
	}
	if resp.ProtoMajor != 2 {
		t.Errorf("protocol %s, want HTTP/2", resp.Proto)
	}
	if echo["xfp"] != "https" || echo["host"] != "myapp.test" {
		t.Errorf("upstream saw %v", echo)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeTLS: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeTLS did not return after cancel")
	}
}

func TestLookup(t *testing.T) {
	p, err := New([]config.Route{{Name: "myapp.test", Port: 1, Wildcard: true}, {Name: "*.w.test", Port: 2}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host         string
		port         int
		wildcard, ok bool
	}{
		{"myapp.test", 1, false, true},
		{"A.MyApp.test:443", 1, true, true},
		{"x.w.test", 2, true, true},
		{"w.test", 0, false, false},
	} {
		r, wc, ok := p.Lookup(tc.host)
		if r.Port != tc.port || wc != tc.wildcard || ok != tc.ok {
			t.Errorf("Lookup(%q) = %d, %v, %v", tc.host, r.Port, wc, ok)
		}
	}
}
