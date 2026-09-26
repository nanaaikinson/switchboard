//go:build integration

package mdns

import (
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

// TestResponderAnswersOnLoopback queries the Go responder over multicast on
// the loopback interface, as a local resolver would.
func TestResponderAnswersOnLoopback(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the Go responder is not used on Linux")
	}
	r, err := openResponder(loopback)
	if err != nil {
		t.Skipf("no multicast on loopback here: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Set([]string{"sb-it-probe.local"}); err != nil {
		t.Fatal(err)
	}

	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ifi, _ := loopback()
	p := ipv4.NewPacketConn(c)
	if err := p.SetMulticastInterface(ifi); err != nil {
		t.Fatal(err)
	}
	_ = p.SetMulticastLoopback(true)
	ask := func(name string) *dns.Msg {
		t.Helper()
		q := new(dns.Msg)
		q.SetQuestion(name, dns.TypeA)
		q.RecursionDesired = false
		buf, _ := q.Pack()
		if _, err := c.WriteToUDP(buf, groupV4); err != nil {
			t.Fatalf("send query: %v", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		in := make([]byte, 1500)
		for {
			n, _, err := c.ReadFromUDP(in)
			if err != nil {
				return nil // no answer
			}
			m := new(dns.Msg)
			if m.Unpack(in[:n]) == nil && m.Response {
				return m
			}
		}
	}

	m := ask("sb-it-probe.local.")
	if m == nil || len(m.Answer) != 1 {
		t.Fatalf("answer = %v, want one A record", m)
	}
	if a, ok := m.Answer[0].(*dns.A); !ok || !a.A.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("answer = %v, want 127.0.0.1", m.Answer[0])
	}

	if err := r.Set(nil); err != nil {
		t.Fatal(err)
	}
	if m := ask("sb-it-probe.local."); m != nil && len(m.Answer) > 0 {
		t.Errorf("withdrawn name still answered: %v", m.Answer)
	}
}
