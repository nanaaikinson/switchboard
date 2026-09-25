package mdns

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePub records what it was asked to announce.
type fakePub struct {
	mu     sync.Mutex
	sets   [][]string
	closed bool
	setErr error
}

func (p *fakePub) Set(names []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sets = append(p.sets, slices.Clone(names))
	return p.setErr
}

func (p *fakePub) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *fakePub) last() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sets) == 0 {
		return nil
	}
	return p.sets[len(p.sets)-1]
}

func (p *fakePub) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func backend(name string, pub *fakePub, err error) Backend {
	return Backend{Name: name, Interface: "loopback", Open: func() (Publisher, error) {
		if err != nil {
			return nil, err
		}
		return pub, nil
	}}
}

// run starts a with a state channel and returns a stop function that waits
// for Run to return.
func run(t *testing.T, a *Announcer) (<-chan State, func()) {
	t.Helper()
	states := make(chan State, 32)
	a.OnChange = func(st State) { states <- st }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return states, stop
}

func waitState(t *testing.T, states <-chan State, ok func(State) bool) State {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case st := <-states:
			if ok(st) {
				return st
			}
		case <-timeout:
			t.Fatal("timed out waiting for announcer state")
		}
	}
}

func TestAnnouncerAnnouncesAndWithdraws(t *testing.T) {
	pub := &fakePub{}
	a := NewAnnouncer(backend("fake", pub, nil))
	states, stop := run(t, a)

	a.Update(true, []string{"B.local.", "a.local", "a.local"})
	st := waitState(t, states, func(st State) bool { return len(st.Announced) == 2 })
	if want := []string{"a.local", "b.local"}; !slices.Equal(st.Announced, want) || st.Backend != "fake" || st.Interface != "loopback" {
		t.Fatalf("state = %+v, want %v announced by fake on loopback", st, want)
	}
	if got := pub.last(); !slices.Equal(got, []string{"a.local", "b.local"}) {
		t.Errorf("published %v", got)
	}

	a.Update(true, []string{"a.local"})
	waitState(t, states, func(st State) bool { return len(st.Announced) == 1 })
	if got := pub.last(); !slices.Equal(got, []string{"a.local"}) {
		t.Errorf("after removal published %v, want [a.local]", got)
	}

	stop()
	if !pub.isClosed() {
		t.Error("publisher not closed when Run returned")
	}
}

func TestAnnouncerDisableClosesBackend(t *testing.T) {
	pub := &fakePub{}
	a := NewAnnouncer(backend("fake", pub, nil))
	states, _ := run(t, a)

	a.Update(true, []string{"a.local"})
	waitState(t, states, func(st State) bool { return len(st.Announced) == 1 })
	a.Update(false, []string{"a.local"})
	st := waitState(t, states, func(st State) bool { return st.Backend == "" })
	if len(st.Announced) != 0 || st.Error != "" {
		t.Errorf("disabled state = %+v, want empty", st)
	}
	if !pub.isClosed() {
		t.Error("publisher not closed when disabled")
	}
}

func TestAnnouncerBackendOrder(t *testing.T) {
	tests := []struct {
		name        string
		errs        []error // per backend; nil opens
		wantBackend string
		wantErr     string
	}{
		{name: "first wins", errs: []error{nil, nil}, wantBackend: "b0"},
		{name: "falls back", errs: []error{errors.New("not running"), nil}, wantBackend: "b1"},
		{name: "none", errs: []error{errors.New("not running"), errors.New("no multicast")}, wantErr: "b0: not running; b1: no multicast"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bs []Backend
			for i, err := range tt.errs {
				bs = append(bs, backend("b"+string(rune('0'+i)), &fakePub{}, err))
			}
			a := NewAnnouncer(bs...)
			a.Retry = time.Hour
			states, _ := run(t, a)
			a.Update(true, []string{"a.local"})
			st := waitState(t, states, func(State) bool { return true })
			if st.Backend != tt.wantBackend || !strings.Contains(st.Error, tt.wantErr) || (tt.wantErr == "") != (st.Error == "") {
				t.Errorf("state = %+v, want backend %q, error %q", st, tt.wantBackend, tt.wantErr)
			}
		})
	}
}

func TestAnnouncerRetriesFailedSet(t *testing.T) {
	pub := &fakePub{setErr: errors.New("daemon restarted")}
	opens := 0
	var mu sync.Mutex
	a := NewAnnouncer(Backend{Name: "fake", Open: func() (Publisher, error) {
		mu.Lock()
		defer mu.Unlock()
		opens++
		if opens > 1 {
			pub.mu.Lock()
			pub.setErr = nil
			pub.mu.Unlock()
		}
		return pub, nil
	}})
	a.Retry = 10 * time.Millisecond
	states, _ := run(t, a)
	a.Update(true, []string{"a.local"})
	st := waitState(t, states, func(st State) bool { return st.Error != "" })
	if !strings.Contains(st.Error, "fake: daemon restarted") {
		t.Errorf("error = %q", st.Error)
	}
	waitState(t, states, func(st State) bool { return st.Error == "" && len(st.Announced) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if opens != 2 {
		t.Errorf("opened %d times, want 2 (reopen after a failed Set)", opens)
	}
}

func TestAnnouncerNoBackends(t *testing.T) {
	a := NewAnnouncer()
	a.Retry = time.Hour
	states, _ := run(t, a)
	a.Update(true, nil)
	st := waitState(t, states, func(State) bool { return true })
	if !strings.Contains(st.Error, "no mDNS responder") {
		t.Errorf("error = %q", st.Error)
	}
}
