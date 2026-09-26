package main

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/mdns"
)

// memPublisher "announces" names in memory.
type memPublisher struct {
	mu    sync.Mutex
	names []string
}

func (p *memPublisher) Set(names []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.names = slices.Clone(names)
	return nil
}

func (p *memPublisher) Close() error { return p.Set(nil) }

func (p *memPublisher) get() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.names)
}

func TestTLDCommands(t *testing.T) {
	configDir(t)
	pub := &memPublisher{}
	startDaemon(t, daemonOptions{mdnsBackends: []mdns.Backend{{
		Name: "mem", Interface: "loopback", Open: func() (mdns.Publisher, error) { return pub, nil },
	}}})

	if out := mustRun(t, "tld", "ls"); !strings.Contains(out, ".test  split DNS    default") || strings.Contains(out, ".local") {
		t.Errorf("tld ls before: %q", out)
	}
	if out, err := run(t, "add", "myapp.local", "3000"); err == nil || !strings.Contains(err.Error(), "sb tld add local --mdns") {
		t.Errorf("add .local while off: %v %q", err, out)
	}
	if _, err := run(t, "tld", "add", "local"); err == nil || !strings.Contains(err.Error(), "--mdns") {
		t.Errorf("tld add local without --mdns: %v", err)
	}
	if _, err := run(t, "tld", "add", "dev", "--mdns"); err == nil || !strings.Contains(err.Error(), "only works for .local") {
		t.Errorf("tld add dev --mdns: %v", err)
	}

	out := mustRun(t, "tld", "add", "local", "--mdns")
	for _, want := range []string{"Added .local (mDNS, EXPERIMENTAL)", "loopback interface only", "Wildcard routes can't be announced",
		"can't sign .local names"} { // the test CA only covers .test
		if !strings.Contains(out, want) {
			t.Errorf("tld add output missing %q:\n%s", want, out)
		}
	}
	if out := mustRun(t, "tld", "add", "local", "--mdns"); out != ".local is already served.\n" {
		t.Errorf("tld add again: %q", out)
	}
	if out := mustRun(t, "tld", "ls"); !strings.Contains(out, ".local  mDNS         experimental; no wildcards") {
		t.Errorf("tld ls after: %q", out)
	}

	mustRun(t, "add", "myapp.local", "3000")
	mustRun(t, "add", "*.tenants.local", "3001")
	mustRun(t, "add", "myapp", "3002")
	deadline := time.Now().Add(3 * time.Second)
	for !slices.Equal(pub.get(), []string{"myapp.local"}) {
		if time.Now().After(deadline) {
			t.Fatalf("announced %v, want [myapp.local]", pub.get())
		}
		time.Sleep(10 * time.Millisecond)
	}

	out = mustRun(t, "ls")
	for _, want := range []string{"MDNS (EXPERIMENTAL)", "announced", "not announced (wildcard)", "Wildcards can't be announced over mDNS"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls missing %q:\n%s", want, out)
		}
	}
	c, _ := newClient()
	st, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !st.MDNS.Enabled || !st.MDNS.Experimental || st.MDNS.Backend != "mem" || st.MDNS.Interface != "loopback" {
		t.Errorf("status mdns = %+v", st.MDNS)
	}
	if b, _ := json.Marshal(st.Routes); !strings.Contains(string(b), `"mdns":"wildcard"`) {
		t.Errorf("routes JSON lacks mdns state: %s", b)
	}

	if _, err := run(t, "tld", "rm", "local"); err == nil || !strings.Contains(err.Error(), "remove them first") {
		t.Errorf("tld rm with routes: %v", err)
	}
	mustRun(t, "rm", "myapp.local")
	mustRun(t, "rm", "*.tenants.local")
	if out := mustRun(t, "tld", "rm", "local"); out != "Removed .local\n" {
		t.Errorf("tld rm: %q", out)
	}
	for len(pub.get()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("still announced after rm: %v", pub.get())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if out := mustRun(t, "ls"); strings.Contains(out, "MDNS") {
		t.Errorf("ls shows MDNS column with .local off:\n%s", out)
	}
}
