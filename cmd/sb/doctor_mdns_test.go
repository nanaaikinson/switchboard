package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/nanaaikinson/switchboard/internal/mdns"
)

// fakeUnicastDNS serves DNS on loopback: every A query gets answer, or
// NXDOMAIN if answer is nil. It becomes the only unicast server doctor asks.
func fakeUnicastDNS(t *testing.T, answer net.IP) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		if answer == nil {
			m.Rcode = dns.RcodeNameError
		} else {
			m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: answer}}
		}
		_ = w.WriteMsg(m)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	orig := unicastServers
	unicastServers = func() ([]string, error) { return []string{pc.LocalAddr().String()}, nil }
	t.Cleanup(func() { unicastServers = orig; _ = srv.Shutdown() })
}

func TestDoctorMDNS(t *testing.T) {
	tests := []struct {
		name        string
		openErr     error
		localDNSErr error
		hijack      net.IP
		want        []string
	}{
		{
			name: "all good",
			want: []string{
				"[PASS] mdns (experimental): announcing 1 .local names via mem on loopback",
				"[PASS] myapp.local: resolves to 127.0.0.1",
				"[PASS] .local lookups: stay on mDNS; unicast DNS doesn't answer them",
			},
		},
		{
			name:    "no responder",
			openErr: errors.New("mDNSResponder is not running"),
			want: []string{
				"[FAIL] mdns (experimental): .local names are not announced: mem: mDNSResponder is not running",
				"       fix: Start the system's mDNS responder (macOS: mDNSResponder; Linux: 'sudo systemctl enable --now avahi-daemon'); the daemon retries every 10 seconds.",
			},
		},
		{
			name:        "resolver leaks",
			localDNSErr: fixErr{"hosts: files dns passes .local names to unicast DNS", "Put mdns4_minimal first."},
			want: []string{
				"[FAIL] .local lookups: hosts: files dns passes .local names to unicast DNS",
				"       fix: Put mdns4_minimal first.",
			},
		},
		{
			name:   "unicast server answers",
			hijack: net.IPv4(203, 0, 113, 7),
			want: []string{
				"-> 203.0.113.7): programs that skip mDNS get another host's address, and the server sees the .local names you look up",
				"       fix: Use .test names for anything that must not leave this machine, or ask your network admin to stop answering .local (it is reserved for mDNS, RFC 6762).",
			},
		},
		{
			name:        "unsupported check, nothing answers",
			localDNSErr: fmt.Errorf("checking is %w", errors.ErrUnsupported),
			want:        []string{"[SKIP] .local lookups: checking is unsupported operation"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir(t)
			fakeUnicastDNS(t, tt.hijack)
			startDaemon(t, daemonOptions{mdnsBackends: []mdns.Backend{{Name: "mem", Interface: "loopback",
				Open: func() (mdns.Publisher, error) {
					if tt.openErr != nil {
						return nil, tt.openErr
					}
					return &memPublisher{}, nil
				}}}})
			mustRun(t, "tld", "add", "local", "--mdns")
			addRoute(t, "", "myapp.local", live(t))
			useDiag(t, fakeDiag{addrs: []string{"127.0.0.1"}, localDNSErr: tt.localDNSErr})

			var out string
			waitFor(t, func() bool {
				out, _ = run(t, "doctor")
				return strings.Contains(out, "announcing 1") || strings.Contains(out, "[FAIL] mdns")
			})
			for _, w := range tt.want {
				if !strings.Contains(out, w+"\n") {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
			if strings.Contains(out, "resolver .local") || strings.Contains(out, "https probe.local") {
				t.Errorf("split DNS checks run for .local:\n%s", out)
			}
		})
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
