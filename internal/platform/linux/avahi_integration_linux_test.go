//go:build integration

package linux

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestAvahiAnnouncesOnLoopback needs avahi-daemon on the system bus and
// nss-mdns. It announces a name and looks it up the way apps do.
func TestAvahiAnnouncesOnLoopback(t *testing.T) {
	b, err := New(Options{}).MDNS()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := b.Open()
	if err != nil {
		t.Skipf("no Avahi here: %v", err)
	}
	defer pub.Close()
	const name = "sb-it-probe.local"
	if err := pub.Set([]string{name}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	lookup := func() string {
		out, _ := exec.Command("getent", "ahosts", name).CombinedOutput()
		return string(out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(lookup(), "127.0.0.1") {
		if time.Now().After(deadline) {
			t.Fatalf("%s does not resolve: %q", name, lookup())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := pub.Set(nil); err != nil {
		t.Fatalf("Set(nil): %v", err)
	}
	time.Sleep(time.Second)
	if out := lookup(); strings.Contains(out, "127.0.0.1") {
		t.Errorf("withdrawn name still resolves: %q", out)
	}
}
