//go:build integration

package darwin

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDNSSDAnnouncesLocally registers a local-only name with the real
// mDNSResponder and resolves it the way apps do. The record goes away with
// the connection.
func TestDNSSDAnnouncesLocally(t *testing.T) {
	d, err := openDNSSD("")
	if err != nil {
		t.Skipf("no mDNSResponder: %v", err)
	}
	defer d.Close()
	const name = "sb-it-dnssd.local"
	if err := d.Set([]string{name}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	lookup := func() string {
		out, _ := exec.Command("dscacheutil", "-q", "host", "-a", "name", name).CombinedOutput()
		return string(out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for out := lookup(); !strings.Contains(out, "ip_address: 127.0.0.1") || !strings.Contains(out, "ipv6_address: ::1"); out = lookup() {
		if time.Now().After(deadline) {
			t.Fatalf("%s does not resolve to 127.0.0.1 and ::1: %q", name, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := d.Set(nil); err != nil {
		t.Fatalf("Set(nil): %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if out := lookup(); strings.Contains(out, "127.0.0.1") {
		t.Errorf("withdrawn name still resolves: %q", out)
	}
}
