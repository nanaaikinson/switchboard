package proxy

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// upstream starts a local app that identifies itself and echoes request details.
func upstream(t *testing.T, id string) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", id)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"host": r.Host,
			"xff":  r.Header.Get("X-Forwarded-For"),
			"xfh":  r.Header.Get("X-Forwarded-Host"),
			"xfp":  r.Header.Get("X-Forwarded-Proto"),
			"fwd":  r.Header.Get("Forwarded"),
			"xrip": r.Header.Get("X-Real-IP"),
			"hop":  strings.Join(r.Header.Values(HopHeader), ","),
		})
	}))
	t.Cleanup(srv.Close)
	return srv, portOf(t, srv.Listener.Addr())
}

func portOf(t *testing.T, a net.Addr) int {
	t.Helper()
	return a.(*net.TCPAddr).Port
}

// front starts the proxy under test in front of the upstreams.
func front(t *testing.T, routes ...config.Route) (*ReverseProxy, *httptest.Server) {
	t.Helper()
	p, err := New(routes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return p, srv
}

func get(t *testing.T, srv *httptest.Server, host string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestRouting(t *testing.T) {
	ids := []string{"app", "api", "wild", "tenants", "other"}
	ports := map[string]int{}
	for _, id := range ids {
		_, ports[id] = upstream(t, id)
	}
	_, fr := front(t,
		config.Route{Name: "myapp.test", Port: ports["app"]},
		config.Route{Name: "api.myapp.test", Port: ports["api"]},
		config.Route{Name: "*.myapp.test", Port: ports["wild"]},
		config.Route{Name: "*.tenants.myapp.test", Port: ports["tenants"]},
		config.Route{Name: "other.test", Port: ports["other"], Wildcard: true},
	)
	tests := []struct {
		host, want string // want "" means 404
	}{
		{"myapp.test", "app"},
		{"MyApp.TEST", "app"},
		{"myapp.test.", "app"},
		{"myapp.test:80", "app"},
		{"api.myapp.test", "api"},              // exact beats *.myapp.test
		{"foo.myapp.test", "wild"},             // wildcard
		{"a.b.myapp.test", "wild"},             // wildcard at depth
		{"acme.tenants.myapp.test", "tenants"}, // longest wildcard wins
		{"x.y.tenants.myapp.test", "tenants"},
		{"tenants.myapp.test", "wild"}, // *.tenants does not match its base
		{"other.test", "other"},        // Wildcard flag: base itself
		{"deep.sub.other.test", "other"},
		{"unknown.test", ""},
		{"myapp.test.evil.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			resp, _ := get(t, fr, tt.host)
			if tt.want == "" {
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("status = %d, want 404", resp.StatusCode)
				}
				return
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got := resp.Header.Get("X-Upstream"); got != tt.want {
				t.Errorf("routed to %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNotFoundPage(t *testing.T) {
	_, fr := front(t,
		config.Route{Name: "myapp.test", Port: 7000},
		config.Route{Name: "api.myapp.test", Port: 7001, Wildcard: true},
	)
	resp, body := get(t, fr, "nope.test")
	if resp.StatusCode != http.StatusNotFound || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{"nope.test", "myapp.test", "7000", "api.myapp.test", "7001", "sb add nope.test"} {
		if !strings.Contains(body, want) {
			t.Errorf("404 page missing %q", want)
		}
	}

	// Foreign hosts (DNS rebinding) must not see the route table.
	_, body = get(t, fr, "evil.com")
	if strings.Contains(body, "myapp") || strings.Contains(body, "7000") {
		t.Errorf("404 for foreign host leaks routes:\n%s", body)
	}
}

func TestForwardedHeaders(t *testing.T) {
	_, port := upstream(t, "app")
	_, fr := front(t, config.Route{Name: "myapp.test", Port: port})
	_, body := get(t, fr, "myapp.test",
		"X-Forwarded-For", "6.6.6.6", "X-Forwarded-Host", "spoofed", "X-Forwarded-Proto", "https",
		"Forwarded", "for=6.6.6.6;proto=https", "X-Real-IP", "6.6.6.6")
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	want := map[string]string{"host": "myapp.test", "xff": "127.0.0.1", "xfh": "myapp.test", "xfp": "http", "fwd": "", "xrip": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestLoopDetected(t *testing.T) {
	_, port := upstream(t, "app")
	p, fr := front(t)
	self := portOf(t, fr.Listener.Addr())
	if err := p.SetRoutes([]config.Route{
		{Name: "loop.test", Port: self}, // the proxy's own port
		{Name: "redirect.test", Port: self, RedirectHTTPS: true},
		{Name: "app.test", Port: port},
	}); err != nil {
		t.Fatal(err)
	}
	redirecting := httptest.NewServer(RedirectHTTPS(p, 8443))
	t.Cleanup(redirecting.Close)

	tests := []struct {
		name    string
		srv     *httptest.Server
		host    string
		hdr     []string
		status  int
		wantHop string // what the upstream saw
	}{
		{"route to own port", fr, "loop.test", nil, http.StatusLoopDetected, ""},
		{"redirecting route to own port", redirecting, "redirect.test", []string{HopHeader, "redirect.test"}, http.StatusLoopDetected, ""},
		{"normal route gets the header", fr, "app.test", nil, 200, "app.test"},
		{"a client's header is replaced", fr, "app.test", []string{HopHeader, "other.test"}, 200, "app.test"},
		{"an app calling another route works", fr, "app.test", []string{HopHeader, "front.test"}, 200, "app.test"},
		{"a client claiming this host is refused", fr, "APP.test.", []string{HopHeader, "app.test"}, http.StatusLoopDetected, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := get(t, tt.srv, tt.host, tt.hdr...)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.status, body)
			}
			if tt.status == http.StatusLoopDetected {
				if !strings.Contains(body, "loops back to Switchboard") || !strings.Contains(body, "sb add") {
					t.Errorf("508 page: %s", body)
				}
				return
			}
			var got map[string]string
			if err := json.Unmarshal([]byte(body), &got); err != nil || got["hop"] != tt.wantHop {
				t.Errorf("upstream saw %s = %q (%v), want %q", HopHeader, got["hop"], err, tt.wantHop)
			}
		})
	}
	if logs := p.Logs("loop.test"); len(logs) != 2 || logs[0].Status != http.StatusLoopDetected {
		t.Errorf("loop.test logs = %+v, want the looped request and the original, both 508", logs)
	}
}

func TestBadGatewayNamesPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portOf(t, ln.Addr())
	_ = ln.Close() // nothing listens here now

	_, fr := front(t, config.Route{Name: "myapp.test", Port: port})
	resp, body := get(t, fr, "myapp.test")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if want := "Nothing is listening on port " + strconv.Itoa(port); !strings.Contains(body, want) {
		t.Errorf("502 page missing %q:\n%s", want, body)
	}
}

func TestStreamingSSE(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: two\n\n")
	}))
	t.Cleanup(up.Close)

	_, fr := front(t, config.Route{Name: "myapp.test", Port: portOf(t, up.Listener.Addr())})
	req, _ := http.NewRequest(http.MethodGet, fr.URL, nil)
	req.Host = "myapp.test"
	resp, err := fr.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	// The first event must arrive while the upstream is still blocked.
	if got := readLine(t, br); got != "data: one" {
		t.Fatalf("first line = %q", got)
	}
	once.Do(func() { close(release) })
	readLine(t, br) // blank separator
	if got := readLine(t, br); got != "data: two" {
		t.Fatalf("second event = %q", got)
	}
}

func readLine(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	c := make(chan res, 1)
	go func() { s, err := br.ReadString('\n'); c <- res{s, err} }()
	select {
	case r := <-c:
		if r.err != nil {
			t.Fatalf("read: %v", r.err)
		}
		return strings.TrimRight(r.s, "\r\n")
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for streamed data; response is buffered")
		return ""
	}
}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func wsAccept(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// wsWrite writes one short text frame, masked when sent by a client.
func wsWrite(w io.Writer, msg string, mask bool) error {
	b := []byte{0x81, byte(len(msg))}
	payload := []byte(msg)
	if mask {
		key := []byte{1, 2, 3, 4}
		b[1] |= 0x80
		b = append(b, key...)
		for i := range payload {
			payload[i] ^= key[i%4]
		}
	}
	_, err := w.Write(append(b, payload...))
	return err
}

// wsRead reads one short frame and returns its unmasked payload.
func wsRead(r io.Reader) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n, masked := int(hdr[1]&0x7f), hdr[1]&0x80 != 0
	if n > 125 {
		return "", errors.New("test codec supports short frames only")
	}
	var key [4]byte
	if masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return "", err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", err
	}
	if masked {
		for i := range payload {
			payload[i] ^= key[i%4]
		}
	}
	return string(payload), nil
}

func TestWebSocketEcho(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			wsAccept(r.Header.Get("Sec-WebSocket-Key")))
		_ = brw.Flush()
		for {
			msg, err := wsRead(brw.Reader)
			if err != nil || wsWrite(conn, msg, false) != nil {
				return
			}
		}
	}))
	t.Cleanup(up.Close)
	px, fr := front(t, config.Route{Name: "myapp.test", Port: portOf(t, up.Listener.Addr())})

	conn, err := net.Dial("tcp", fr.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: myapp.test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != wsAccept(key) {
		t.Fatalf("Sec-WebSocket-Accept = %q, want %q", got, wsAccept(key))
	}
	for _, msg := range []string{"hello", "second message"} {
		if err := wsWrite(conn, msg, true); err != nil {
			t.Fatal(err)
		}
		got, err := wsRead(br)
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if got != msg {
			t.Errorf("echo = %q, want %q", got, msg)
		}
	}
	conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for len(px.Logs("myapp.test")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond) // logged when the tunnel closes
	}
	if logs := px.Logs("myapp.test"); len(logs) != 1 || logs[0].Status != http.StatusSwitchingProtocols {
		t.Errorf("WebSocket logs = %+v, want one 101", logs)
	}
}

func TestConcurrentRouteSwap(t *testing.T) {
	_, portA := upstream(t, "A")
	_, portB := upstream(t, "B")
	routesA := []config.Route{{Name: "myapp.test", Port: portA}}
	routesB := []config.Route{{Name: "myapp.test", Port: portB}, {Name: "*.myapp.test", Port: portB}}
	p, fr := front(t, routesA...)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				req, _ := http.NewRequest(http.MethodGet, fr.URL, nil)
				req.Host = "myapp.test"
				resp, err := fr.Client().Do(req)
				if err != nil {
					t.Errorf("worker %d: %v", w, err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if id := resp.Header.Get("X-Upstream"); resp.StatusCode != http.StatusOK || (id != "A" && id != "B") {
					t.Errorf("worker %d: status %d upstream %q during swap", w, resp.StatusCode, id)
					return
				}
			}
		}()
	}
	for i := range 200 {
		routes := routesA
		if i%2 == 0 {
			routes = routesB
		}
		if err := p.SetRoutes(routes); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	if err := p.SetRoutes(routesB); err != nil {
		t.Fatal(err)
	}
	if resp, _ := get(t, fr, "x.myapp.test"); resp.Header.Get("X-Upstream") != "B" {
		t.Errorf("after final swap: upstream %q, want B", resp.Header.Get("X-Upstream"))
	}
}

func TestSetRoutesRejectsInvalid(t *testing.T) {
	good := []config.Route{{Name: "myapp.test", Port: 7000}}
	p, err := New(good)
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string][]config.Route{
		"bad port":           {{Name: "a.test", Port: 0}},
		"empty name":         {{Name: "", Port: 1}},
		"duplicate":          {{Name: "a.test", Port: 1}, {Name: "A.test", Port: 2}},
		"wildcard conflict":  {{Name: "a.test", Port: 1, Wildcard: true}, {Name: "*.a.test", Port: 2}},
		"normalized collide": {{Name: "a.test", Port: 1}, {Name: "a.test.", Port: 2}},
	}
	for name, routes := range bad {
		t.Run(name, func(t *testing.T) {
			if err := p.SetRoutes(routes); err == nil {
				t.Fatal("expected error")
			}
			if got := p.Routes(); len(got) != 1 || got[0] != good[0] {
				t.Errorf("routes changed after failed SetRoutes: %v", got)
			}
		})
	}
}

func TestListenAndServe(t *testing.T) {
	addrs := []string{"127.0.0.1:0"}
	if ln, err := net.Listen("tcp", "[::1]:0"); err == nil {
		_ = ln.Close()
		addrs = append(addrs, "[::1]:0")
	}
	lns, err := Listen(addrs)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	_, port := upstream(t, "app")
	p, err := New([]config.Route{{Name: "myapp.test", Port: port}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, p, lns) }()

	client := &http.Client{Timeout: 3 * time.Second}
	for _, ln := range lns {
		req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		req.Host = "myapp.test"
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET via %s: %v", ln.Addr(), err)
		}
		resp.Body.Close()
		if resp.Header.Get("X-Upstream") != "app" {
			t.Errorf("via %s: upstream %q", ln.Addr(), resp.Header.Get("X-Upstream"))
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	for _, ln := range lns {
		if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
			c.Close()
			t.Errorf("%s still accepting after Serve returned", ln.Addr())
		}
	}
}

func TestListenRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{":80", "0.0.0.0:80", "192.168.1.10:80", "localhost:80", "127.0.0.1"} {
		if _, err := Listen([]string{addr}); err == nil {
			t.Errorf("Listen(%q) succeeded, want error", addr)
		}
	}
	if err := Serve(context.Background(), http.NotFoundHandler(), nil); err == nil {
		t.Error("Serve with no listeners succeeded, want error")
	}
}
