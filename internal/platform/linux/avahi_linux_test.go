package linux

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// fakeAvahi records entry groups as Avahi would hold them.
type fakeAvahi struct {
	n       int
	groups  map[dbus.ObjectPath][]string // "iface proto flags name addr"
	commits map[dbus.ObjectPath]bool
	addErr  error
	closed  bool
	lost    chan struct{}
}

func newFakeAvahi() *fakeAvahi {
	return &fakeAvahi{groups: map[dbus.ObjectPath][]string{}, commits: map[dbus.ObjectPath]bool{}, lost: make(chan struct{})}
}

func (f *fakeAvahi) EntryGroupNew() (dbus.ObjectPath, error) {
	f.n++
	g := dbus.ObjectPath(fmt.Sprintf("/Client1/EntryGroup%d", f.n))
	f.groups[g] = nil
	return g, nil
}

func (f *fakeAvahi) AddAddress(g dbus.ObjectPath, iface, proto int32, flags uint32, name, addr string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.groups[g] = append(f.groups[g], fmt.Sprintf("%d %d %d %s %s", iface, proto, flags, name, addr))
	return nil
}

func (f *fakeAvahi) Commit(g dbus.ObjectPath) error { f.commits[g] = true; return nil }
func (f *fakeAvahi) Free(g dbus.ObjectPath) error   { delete(f.groups, g); return nil }
func (f *fakeAvahi) Lost() <-chan struct{}          { return f.lost }
func (f *fakeAvahi) Close() error                   { f.closed = true; return nil }

// published lists the committed records, sorted.
func (f *fakeAvahi) published() []string {
	var out []string
	for g, recs := range f.groups {
		if f.commits[g] {
			out = append(out, recs...)
		}
	}
	slices.Sort(out)
	return out
}

func TestAvahiPublishesOnLoopback(t *testing.T) {
	bus := newFakeAvahi()
	a := newAvahi(bus, 1)
	if err := a.Set([]string{"myapp.local"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"1 0 16 myapp.local 127.0.0.1", "1 1 16 myapp.local ::1"}
	if got := bus.published(); !slices.Equal(got, want) {
		t.Fatalf("published %v, want %v (loopback index, no reverse records)", got, want)
	}
	var first dbus.ObjectPath
	for g := range bus.groups {
		first = g
	}

	if err := a.Set([]string{"myapp.local", "api.local"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := bus.groups[first]; !ok {
		t.Error("adding a name replaced the existing name's group")
	}
	if err := a.Set([]string{"api.local"}); err != nil {
		t.Fatal(err)
	}
	if got := bus.published(); len(got) != 2 || !strings.Contains(got[0], "api.local") {
		t.Errorf("after removal published %v, want only api.local", got)
	}

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if len(bus.groups) != 0 || !bus.closed {
		t.Errorf("Close left groups %v, closed=%v", bus.groups, bus.closed)
	}
}

func TestAvahiFailedAddFreesGroup(t *testing.T) {
	bus := newFakeAvahi()
	bus.addErr = errors.New("org.freedesktop.Avahi.InvalidHostNameError")
	a := newAvahi(bus, 1)
	err := a.Set([]string{"bad.local"})
	if err == nil || !strings.Contains(err.Error(), "announce bad.local") {
		t.Fatalf("err = %v", err)
	}
	if len(bus.groups) != 0 {
		t.Errorf("failed group not freed: %v", bus.groups)
	}
}
