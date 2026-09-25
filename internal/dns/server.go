package dns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/miekg/dns"
)

// ttl is short so TLD changes take effect quickly in OS caches.
const ttl = 1

// DefaultTLDs returns the TLDs served when none are configured.
func DefaultTLDs() []string { return []string{"test"} }

var (
	loopbackV4 = net.IPv4(127, 0, 0, 1).To4()
	loopbackV6 = net.IPv6loopback
)

// Server answers A and AAAA queries for names under its TLDs with loopback
// addresses and refuses everything else. It never forwards queries.
type Server struct {
	addr string
	tlds atomic.Pointer[[]string] // normalized, e.g. "test"

	pc  net.PacketConn
	ln  net.Listener
	udp *dns.Server
	tcp *dns.Server
}

// New returns a server for addr (a loopback host:port) and tlds.
// Empty addr or tlds use DefaultAddr and DefaultTLDs.
func New(addr string, tlds []string) (*Server, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	if err := checkLoopback(addr); err != nil {
		return nil, err
	}
	if len(tlds) == 0 {
		tlds = DefaultTLDs()
	}
	s := &Server{addr: addr}
	if err := s.SetTLDs(tlds); err != nil {
		return nil, err
	}
	return s, nil
}

// SetTLDs replaces the served TLDs. Safe to call while serving.
func (s *Server) SetTLDs(tlds []string) error {
	norm, err := normalizeTLDs(tlds)
	if err != nil {
		return err
	}
	s.tlds.Store(&norm)
	return nil
}

// TLDs returns a copy of the served TLDs.
func (s *Server) TLDs() []string {
	return append([]string(nil), *s.tlds.Load()...)
}

// Listen binds UDP and TCP on the same port. With port 0, UDP picks the port.
func (s *Server) Listen() error {
	host, want, _ := net.SplitHostPort(s.addr)
	// UDP and TCP share one port number. With port 0 the UDP port is random,
	// and that number may be taken for TCP; then try another.
	attempts := 1
	if want == "0" {
		attempts = 10
	}
	var err error
	for range attempts {
		var pc net.PacketConn
		pc, err = net.ListenPacket("udp", s.addr)
		if err != nil {
			return fmt.Errorf("dns: listen udp %s: %w; is another DNS server using this port?", s.addr, err)
		}
		_, port, _ := net.SplitHostPort(pc.LocalAddr().String())
		var ln net.Listener
		ln, err = listenTCP("tcp", net.JoinHostPort(host, port))
		if err != nil {
			_ = pc.Close()
			err = fmt.Errorf("dns: listen tcp %s: %w", net.JoinHostPort(host, port), err)
			continue
		}
		s.pc, s.ln = pc, ln
		s.udp = &dns.Server{PacketConn: pc, Handler: s}
		s.tcp = &dns.Server{Listener: ln, Handler: s}
		return nil
	}
	return err
}

// listenTCP is net.Listen; swapped in tests.
var listenTCP = net.Listen

// Addr returns the bound address after Listen, else the configured one.
func (s *Server) Addr() string {
	if s.pc != nil {
		return s.pc.LocalAddr().String()
	}
	return s.addr
}

// Serve answers queries until ctx is done or a listener fails. Call Listen first.
func (s *Server) Serve(ctx context.Context) error {
	if s.pc == nil {
		return errors.New("dns: Serve called before Listen")
	}
	slog.Info("dns server started", "addr", s.Addr(), "tlds", len(s.TLDs()))

	errc := make(chan error, 2)
	var wg sync.WaitGroup
	for _, srv := range []*dns.Server{s.udp, s.tcp} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errc <- srv.ActivateAndServe()
		}()
	}

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	_ = s.pc.Close()
	_ = s.ln.Close()
	wg.Wait()
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("dns: serve: %w", err)
	}
	return nil
}

// ServeDNS implements dns.Handler.
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := s.answer(r)
	if err := w.WriteMsg(m); err != nil {
		slog.Debug("dns write failed", "err", err)
	}
}

func (s *Server) answer(r *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = false
	if opt := r.IsEdns0(); opt != nil {
		m.SetEdns0(opt.UDPSize(), false)
	}
	if r.Opcode != dns.OpcodeQuery || len(r.Question) != 1 {
		m.Rcode = dns.RcodeRefused
		return m
	}

	q := r.Question[0]
	name := strings.ToLower(q.Name)
	tld, ok := s.match(name)
	if !ok || q.Qclass != dns.ClassINET {
		m.Rcode = dns.RcodeRefused
		return m
	}
	m.Authoritative = true
	if name == tld+"." {
		return m // bare TLD: in our zone, but no address records (NODATA)
	}

	hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: ttl, Rrtype: q.Qtype}
	switch q.Qtype {
	case dns.TypeA:
		m.Answer = []dns.RR{&dns.A{Hdr: hdr, A: loopbackV4}}
	case dns.TypeAAAA:
		m.Answer = []dns.RR{&dns.AAAA{Hdr: hdr, AAAA: loopbackV6}}
	}
	return m // other types: NODATA
}

// match reports the served TLD that fqdn (lowercase, trailing dot) falls under.
func (s *Server) match(fqdn string) (string, bool) {
	for _, tld := range *s.tlds.Load() {
		if fqdn == tld+"." || strings.HasSuffix(fqdn, "."+tld+".") {
			return tld, true
		}
	}
	return "", false
}

func normalizeTLDs(tlds []string) ([]string, error) {
	if len(tlds) == 0 {
		return nil, errors.New("dns: at least one TLD is required")
	}
	seen := make(map[string]bool, len(tlds))
	out := make([]string, 0, len(tlds))
	for _, t := range tlds {
		n := strings.Trim(strings.ToLower(strings.TrimSpace(t)), ".")
		if !validName(n) {
			return nil, fmt.Errorf("dns: invalid TLD %q; use a name like \"test\"", t)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// validName reports whether n is dot-separated LDH labels (letters, digits,
// hyphens; no leading or trailing hyphen; 1-63 bytes each).
func validName(n string) bool {
	if n == "" || len(n) > 253 {
		return false
	}
	for _, label := range strings.Split(n, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("dns: invalid address %q: %w; use host:port like %s", addr, err, DefaultAddr)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("dns: address %q is not a loopback IP; use 127.0.0.1 or ::1", addr)
	}
	return nil
}
