package mdns

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	hmdns "github.com/hashicorp/mdns"
	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

var (
	groupV4 = &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353}
	groupV6 = &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353}

	loopbackV4 = net.IPv4(127, 0, 0, 1).To4()
	loopbackV6 = net.IPv6loopback
)

// GoResponder is the built-in responder: it answers mDNS queries for the
// announced names on the loopback interface, and sends announcements and
// goodbyes there when names change. It is the fallback for systems without a
// usable OS responder; resolvers that don't query over loopback won't see it.
func GoResponder() Backend {
	return Backend{Name: "go", Interface: "loopback", Open: func() (Publisher, error) { return openResponder(loopback) }}
}

// loopback finds the loopback interface; swapped in tests.
var loopback = func() (*net.Interface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagLoopback != 0 && ifi.Flags&net.FlagUp != 0 {
			return &ifi, nil
		}
	}
	return nil, errors.New("no loopback interface is up")
}

type responder struct {
	zone    *zone
	srv     *hmdns.Server
	senders []*net.UDPConn
	ifi     *net.Interface

	mu        sync.Mutex // serializes Set and Close
	announced []string
}

func openResponder(find func() (*net.Interface, error)) (*responder, error) {
	if runtime.GOOS == "linux" {
		// Linux delivers multicast to every socket on the port, whatever
		// interface it joined on, so answers could reach the network. Its
		// loopback also has no multicast by default.
		return nil, errors.New("the built-in responder can't be limited to loopback on Linux; install and start avahi-daemon")
	}
	ifi, err := find()
	if err != nil {
		return nil, err
	}
	if ifi.Flags&net.FlagMulticast == 0 {
		return nil, fmt.Errorf("loopback interface %s has no multicast support", ifi.Name)
	}
	z := &zone{}
	z.names.Store(&map[string]bool{})
	srv, err := hmdns.NewServer(&hmdns.Config{
		Zone:   z,
		Iface:  ifi,
		Logger: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	})
	if err != nil {
		return nil, fmt.Errorf("listen for mDNS on %s: %w; is port 5353 blocked?", ifi.Name, err)
	}
	r := &responder{zone: z, srv: srv, ifi: ifi}
	r.senders = openSenders(ifi)
	return r, nil
}

// openSenders opens sockets for unsolicited announcements, which must come
// from port 5353. Multicast loopback is turned on so that responders on this
// machine hear them. Without any, names are still answered when queried.
func openSenders(ifi *net.Interface) []*net.UDPConn {
	var out []*net.UDPConn
	if c, err := net.ListenMulticastUDP("udp4", ifi, groupV4); err == nil {
		p := ipv4.NewPacketConn(c)
		_ = p.SetMulticastLoopback(true)
		_ = p.SetMulticastInterface(ifi)
		out = append(out, c)
	} else {
		slog.Debug("mdns: no IPv4 announcement socket", "err", err)
	}
	if c, err := net.ListenMulticastUDP("udp6", ifi, groupV6); err == nil {
		p := ipv6.NewPacketConn(c)
		_ = p.SetMulticastLoopback(true)
		_ = p.SetMulticastInterface(ifi)
		out = append(out, c)
	} else {
		slog.Debug("mdns: no IPv6 announcement socket", "err", err)
	}
	return out
}

// Set implements Publisher.
func (r *responder) Set(names []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]bool, len(names))
	for _, n := range names {
		next[dns.Fqdn(n)] = true
	}
	var added, gone []string
	for _, n := range names {
		if !slices.Contains(r.announced, n) {
			added = append(added, n)
		}
	}
	for _, n := range r.announced {
		if !next[dns.Fqdn(n)] {
			gone = append(gone, n)
		}
	}
	r.zone.names.Store(&next)
	r.announced = slices.Clone(names)
	r.send(gone, 0)
	r.send(added, TTL)
	return nil
}

// Close implements Publisher.
func (r *responder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.zone.names.Store(&map[string]bool{})
	r.send(r.announced, 0)
	r.announced = nil
	err := r.srv.Shutdown()
	for _, c := range r.senders {
		_ = c.Close()
	}
	return err
}

// send multicasts records for names: an announcement, or with ttl 0 a
// goodbye. Best effort: the records are also answered when queried.
func (r *responder) send(names []string, ttl uint32) {
	if len(names) == 0 {
		return
	}
	m := new(dns.Msg)
	m.Response, m.Authoritative = true, true
	for _, n := range names {
		m.Answer = append(m.Answer, records(dns.Fqdn(n), ttl, ttl > 0)...)
	}
	buf, err := m.Pack()
	if err != nil {
		slog.Debug("mdns: pack announcement", "err", err)
		return
	}
	for _, c := range r.senders {
		dst := groupV4
		if c.LocalAddr().(*net.UDPAddr).IP.To4() == nil {
			dst = &net.UDPAddr{IP: groupV6.IP, Port: groupV6.Port, Zone: r.ifi.Name}
		}
		if _, err := c.WriteToUDP(buf, dst); err != nil {
			slog.Debug("mdns: send announcement", "err", err)
		}
	}
}

// zone answers A, AAAA and ANY questions for the announced names.
type zone struct {
	names atomic.Pointer[map[string]bool] // FQDNs, lowercase
}

// Records implements hmdns.Zone.
func (z *zone) Records(q dns.Question) []dns.RR {
	name := strings.ToLower(q.Name)
	if !(*z.names.Load())[name] {
		return nil
	}
	rrs := records(name, TTL, true)
	switch q.Qtype {
	case dns.TypeA:
		return rrs[:1]
	case dns.TypeAAAA:
		return rrs[1:]
	case dns.TypeANY:
		return rrs
	}
	return nil
}

// records returns the A and AAAA records for name. unique sets the
// cache-flush bit: Switchboard is the only source of these names.
func records(name string, ttl uint32, unique bool) []dns.RR {
	class := uint16(dns.ClassINET)
	if unique {
		class |= 1 << 15
	}
	return []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: class, Ttl: ttl}, A: loopbackV4},
		&dns.AAAA{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: class, Ttl: ttl}, AAAA: loopbackV6},
	}
}
