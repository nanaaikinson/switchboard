package dns

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// start runs a server on a random loopback port for the duration of the test.
func start(t *testing.T, tlds ...string) *Server {
	t.Helper()
	s, err := New("127.0.0.1:0", tlds)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return s
}

func exchange(network, addr, name string, qtype uint16) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	c := &dns.Client{Net: network, Timeout: 2 * time.Second}
	r, _, err := c.Exchange(m, addr)
	return r, err
}

func query(t *testing.T, network, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	r, err := exchange(network, addr, name, qtype)
	if err != nil {
		t.Fatalf("exchange %s %s over %s: %v", name, dns.TypeToString[qtype], network, err)
	}
	return r
}

// answerIP returns the single A/AAAA address in r, or "" if there is none.
func answerIP(t *testing.T, r *dns.Msg) string {
	t.Helper()
	switch len(r.Answer) {
	case 0:
		return ""
	case 1:
	default:
		t.Fatalf("got %d answers, want at most 1", len(r.Answer))
	}
	switch rr := r.Answer[0].(type) {
	case *dns.A:
		return rr.A.String()
	case *dns.AAAA:
		return rr.AAAA.String()
	}
	t.Fatalf("unexpected answer type %T", r.Answer[0])
	return ""
}

func TestServe(t *testing.T) {
	s := start(t, "test", "internal")
	tests := []struct {
		name   string
		net    string
		qname  string
		qtype  uint16
		rcode  int
		wantIP string
	}{
		{"exact name A", "udp", "myapp.test", dns.TypeA, dns.RcodeSuccess, "127.0.0.1"},
		{"exact name AAAA", "udp", "myapp.test", dns.TypeAAAA, dns.RcodeSuccess, "::1"},
		{"deep subdomain", "udp", "a.b.c.api.myapp.test", dns.TypeA, dns.RcodeSuccess, "127.0.0.1"},
		{"second TLD", "udp", "myapp.internal", dns.TypeA, dns.RcodeSuccess, "127.0.0.1"},
		{"case-insensitive", "udp", "MyApp.TeSt", dns.TypeA, dns.RcodeSuccess, "127.0.0.1"},
		{"case-insensitive AAAA", "udp", "API.MYAPP.TEST", dns.TypeAAAA, dns.RcodeSuccess, "::1"},
		{"TCP fallback A", "tcp", "myapp.test", dns.TypeA, dns.RcodeSuccess, "127.0.0.1"},
		{"TCP fallback AAAA", "tcp", "x.y.myapp.test", dns.TypeAAAA, dns.RcodeSuccess, "::1"},
		{"TCP refused", "tcp", "example.com", dns.TypeA, dns.RcodeRefused, ""},
		{"bare TLD is NODATA", "udp", "test", dns.TypeA, dns.RcodeSuccess, ""},
		{"other type is NODATA", "udp", "myapp.test", dns.TypeMX, dns.RcodeSuccess, ""},
		{"other TLD refused", "udp", "example.com", dns.TypeA, dns.RcodeRefused, ""},
		{".local refused", "udp", "myapp.local", dns.TypeA, dns.RcodeRefused, ""},
		{"suffix without dot refused", "udp", "attest", dns.TypeA, dns.RcodeRefused, ""},
		{"TLD as inner label refused", "udp", "myapp.test.com", dns.TypeA, dns.RcodeRefused, ""},
		{"root refused", "udp", ".", dns.TypeNS, dns.RcodeRefused, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := query(t, tt.net, s.Addr(), tt.qname, tt.qtype)
			if r.Rcode != tt.rcode {
				t.Fatalf("rcode = %s, want %s", dns.RcodeToString[r.Rcode], dns.RcodeToString[tt.rcode])
			}
			if r.RecursionAvailable {
				t.Error("RA set; server must never recurse")
			}
			if got := answerIP(t, r); got != tt.wantIP {
				t.Errorf("answer = %q, want %q", got, tt.wantIP)
			}
			if tt.rcode == dns.RcodeSuccess && !r.Authoritative {
				t.Error("in-zone answer not authoritative")
			}
			if tt.wantIP != "" && !strings.EqualFold(r.Answer[0].Header().Name, dns.Fqdn(tt.qname)) {
				t.Errorf("answer name = %q, want %q", r.Answer[0].Header().Name, dns.Fqdn(tt.qname))
			}
		})
	}
}

func TestSetTLDsAtRuntime(t *testing.T) {
	s := start(t)
	if got := query(t, "udp", s.Addr(), "myapp.test", dns.TypeA).Rcode; got != dns.RcodeSuccess {
		t.Fatalf("before: rcode = %s, want NOERROR", dns.RcodeToString[got])
	}
	if err := s.SetTLDs([]string{"localhost"}); err != nil {
		t.Fatalf("SetTLDs: %v", err)
	}
	if got := query(t, "udp", s.Addr(), "myapp.test", dns.TypeA).Rcode; got != dns.RcodeRefused {
		t.Errorf("old TLD: rcode = %s, want REFUSED", dns.RcodeToString[got])
	}
	if got := query(t, "tcp", s.Addr(), "myapp.localhost", dns.TypeA).Rcode; got != dns.RcodeSuccess {
		t.Errorf("new TLD: rcode = %s, want NOERROR", dns.RcodeToString[got])
	}
}

func TestSetTLDsConcurrentWithQueries(t *testing.T) {
	s := start(t)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				r, err := exchange("udp", s.Addr(), "myapp.test", dns.TypeA)
				if err != nil {
					t.Errorf("worker %d: %v", i, err)
					return
				}
				if r.Rcode != dns.RcodeSuccess && r.Rcode != dns.RcodeRefused {
					t.Errorf("worker %d: rcode = %s", i, dns.RcodeToString[r.Rcode])
				}
			}
		}()
	}
	for i := range 50 {
		tlds := []string{"test"}
		if i%2 == 1 {
			tlds = []string{"internal"}
		}
		if err := s.SetTLDs(tlds); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestNew(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		tlds     []string
		wantAddr string
		wantTLDs []string
		wantErr  string
	}{
		{name: "defaults", wantAddr: DefaultAddr, wantTLDs: []string{"test"}},
		{name: "ipv6 loopback", addr: "[::1]:15353", tlds: []string{"test"}, wantAddr: "[::1]:15353", wantTLDs: []string{"test"}},
		{name: "normalizes TLDs", addr: DefaultAddr, tlds: []string{" .Test. ", "test", "Dev.Internal"}, wantAddr: DefaultAddr, wantTLDs: []string{"test", "dev.internal"}},
		{name: "all interfaces rejected", addr: ":15353", wantErr: "not a loopback"},
		{name: "LAN IP rejected", addr: "192.168.1.10:15353", wantErr: "not a loopback"},
		{name: "hostname rejected", addr: "localhost:15353", wantErr: "not a loopback"},
		{name: "missing port", addr: "127.0.0.1", wantErr: "invalid address"},
		{name: "empty TLD", tlds: []string{"."}, wantErr: "invalid TLD"},
		{name: "bad chars", tlds: []string{"te_st"}, wantErr: "invalid TLD"},
		{name: "leading hyphen", tlds: []string{"-test"}, wantErr: "invalid TLD"},
		{name: "empty label", tlds: []string{"a..test"}, wantErr: "invalid TLD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := New(tt.addr, tt.tlds)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if s.Addr() != tt.wantAddr {
				t.Errorf("Addr() = %q, want %q", s.Addr(), tt.wantAddr)
			}
			if !reflect.DeepEqual(s.TLDs(), tt.wantTLDs) {
				t.Errorf("TLDs() = %v, want %v", s.TLDs(), tt.wantTLDs)
			}
		})
	}
}

func TestSetTLDsRejectsInvalidAndKeepsOld(t *testing.T) {
	s, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{nil, {""}, {"bad tld"}} {
		if err := s.SetTLDs(bad); err == nil {
			t.Errorf("SetTLDs(%q) succeeded, want error", bad)
		}
	}
	if got := s.TLDs(); !reflect.DeepEqual(got, []string{"test"}) {
		t.Errorf("TLDs() = %v after failed updates, want [test]", got)
	}
}

func TestListenSharesPortAndServeStops(t *testing.T) {
	s, err := New("127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Serve(context.Background()); err == nil {
		t.Error("Serve before Listen succeeded, want error")
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	if s.pc.LocalAddr().String() != s.ln.Addr().String() {
		t.Fatalf("udp %s and tcp %s differ", s.pc.LocalAddr(), s.ln.Addr())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	query(t, "tcp", s.Addr(), "myapp.test", dns.TypeA)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if _, err := net.DialTimeout("tcp", s.Addr(), time.Second); err == nil {
		t.Error("TCP port still accepting after Serve returned")
	}
}

// With port 0, the random UDP port's number can be taken for TCP. Listen must
// then try another port instead of failing (this made daemon tests flaky).
func TestListenRetriesWhenTCPPortIsTaken(t *testing.T) {
	orig := listenTCP
	t.Cleanup(func() { listenTCP = orig })
	fails := 3
	listenTCP = func(network, addr string) (net.Listener, error) {
		if fails > 0 {
			fails--
			return nil, fmt.Errorf("listen %s %s: bind: address already in use", network, addr)
		}
		return orig(network, addr)
	}
	s, err := New("127.0.0.1:0", []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer s.pc.Close()
	defer s.ln.Close()
	if fails != 0 {
		t.Errorf("%d TCP failures left, want every one retried", fails)
	}
	if s.pc.LocalAddr().(*net.UDPAddr).Port != s.ln.Addr().(*net.TCPAddr).Port {
		t.Errorf("UDP %s and TCP %s ports differ", s.pc.LocalAddr(), s.ln.Addr())
	}

	// A fixed port is tried once, with a clear error.
	fails = 1
	fixed, err := New("127.0.0.1:"+strconv.Itoa(freeUDPPort(t)), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixed.Listen(); err == nil || !strings.Contains(err.Error(), "listen tcp") {
		t.Errorf("fixed port: %v, want the TCP error", err)
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

// The daemon serves on sockets the helper bound, on a privileged port.
func TestUseServesOnGivenSockets(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New("", []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Use(pc, ln); err != nil {
		t.Fatal(err)
	}
	if s.Addr() != pc.LocalAddr().String() {
		t.Errorf("Addr = %s, want %s", s.Addr(), pc.LocalAddr())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	if got := answerIP(t, query(t, "udp", pc.LocalAddr().String(), "myapp.test", dns.TypeA)); got != "127.0.0.1" {
		t.Errorf("udp answer %q", got)
	}
	if got := answerIP(t, query(t, "tcp", ln.Addr().String(), "myapp.test", dns.TypeA)); got != "127.0.0.1" {
		t.Errorf("tcp answer %q", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	wide, err := net.ListenPacket("udp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wide.Close()
	if err := s.Use(wide, ln); err == nil {
		t.Error("Use accepted a socket beyond loopback")
	}
}
