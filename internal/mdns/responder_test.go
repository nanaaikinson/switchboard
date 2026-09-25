package mdns

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestZoneRecords(t *testing.T) {
	z := &zone{}
	z.names.Store(&map[string]bool{"myapp.local.": true})
	tests := []struct {
		name  string
		q     dns.Question
		types []uint16
	}{
		{"A", dns.Question{Name: "myapp.local.", Qtype: dns.TypeA}, []uint16{dns.TypeA}},
		{"AAAA", dns.Question{Name: "myapp.local.", Qtype: dns.TypeAAAA}, []uint16{dns.TypeAAAA}},
		{"ANY", dns.Question{Name: "myapp.local.", Qtype: dns.TypeANY}, []uint16{dns.TypeA, dns.TypeAAAA}},
		{"case-insensitive", dns.Question{Name: "MyApp.Local.", Qtype: dns.TypeA}, []uint16{dns.TypeA}},
		{"other type", dns.Question{Name: "myapp.local.", Qtype: dns.TypeTXT}, nil},
		{"other name", dns.Question{Name: "printer.local.", Qtype: dns.TypeA}, nil},
		{"subdomain", dns.Question{Name: "api.myapp.local.", Qtype: dns.TypeA}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rrs := z.Records(tt.q)
			if len(rrs) != len(tt.types) {
				t.Fatalf("got %d records %v, want types %v", len(rrs), rrs, tt.types)
			}
			for i, rr := range rrs {
				h := rr.Header()
				if h.Rrtype != tt.types[i] || h.Ttl != TTL || h.Class != dns.ClassINET|1<<15 {
					t.Errorf("record %d = %v, want type %d, ttl %d, cache-flush IN", i, rr, tt.types[i], TTL)
				}
			}
			for _, rr := range rrs {
				var ip net.IP
				switch r := rr.(type) {
				case *dns.A:
					ip = r.A
				case *dns.AAAA:
					ip = r.AAAA
				}
				if !ip.IsLoopback() {
					t.Errorf("%v does not point at loopback", rr)
				}
			}
		})
	}
}

func TestRecordsGoodbye(t *testing.T) {
	for _, rr := range records("myapp.local.", 0, false) {
		if h := rr.Header(); h.Ttl != 0 || h.Class != dns.ClassINET {
			t.Errorf("goodbye record %v: want ttl 0 without cache-flush", rr)
		}
	}
}

func TestOpenResponderRejectsNoMulticast(t *testing.T) {
	find := func() (*net.Interface, error) {
		return &net.Interface{Name: "lo", Flags: net.FlagUp | net.FlagLoopback}, nil
	}
	if _, err := openResponder(find); err == nil {
		t.Fatal("openResponder succeeded on a loopback without multicast")
	}
}
