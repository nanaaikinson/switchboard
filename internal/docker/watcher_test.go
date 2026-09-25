package docker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeDocker is a Docker engine in memory.
type fakeDocker struct {
	mu         sync.Mutex
	containers []Container
	lists      int
	events     chan Event
	errc       chan error
}

func newFake(cs ...Container) *fakeDocker {
	return &fakeDocker{containers: cs, events: make(chan Event, 16), errc: make(chan error, 1)}
}

func (f *fakeDocker) Containers(context.Context) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return append([]Container(nil), f.containers...), nil
}

func (f *fakeDocker) Events(context.Context) (<-chan Event, <-chan error) { return f.events, f.errc }

func (f *fakeDocker) set(cs ...Container) {
	f.mu.Lock()
	f.containers = cs
	f.mu.Unlock()
}

func (f *fakeDocker) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

type harness struct {
	states chan State
	cancel context.CancelFunc
	done   chan struct{}
}

func run(t *testing.T, connect func(context.Context) (Client, string, error)) *harness {
	t.Helper()
	h := &harness{states: make(chan State, 32), done: make(chan struct{})}
	w := &Watcher{Connect: connect, TLDs: tlds, Retry: 10 * time.Millisecond, Debounce: 30 * time.Millisecond,
		OnChange: func(s State) { h.states <- s }}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { w.Run(ctx); close(h.done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return h
}

func (h *harness) next(t *testing.T) State {
	t.Helper()
	select {
	case s := <-h.states:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("no state change")
		return State{}
	}
}

func names(s State) string {
	var out string
	for _, r := range s.Routes {
		out += r.Name + " "
	}
	return out
}

func TestWatcherRetriesUntilDockerStarts(t *testing.T) {
	fake := newFake(ctr("web", nil, pub(8080, 80)))
	var mu sync.Mutex
	attempts := 0
	h := run(t, func(context.Context) (Client, string, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts < 4 {
			return nil, "", errors.New("no container engine running")
		}
		return fake, "unix:///fake.sock", nil
	})
	// Repeated identical failures are reported once.
	if s := h.next(t); s.Connected || s.Err != "no container engine running" || len(s.Routes) != 0 {
		t.Fatalf("first state %+v", s)
	}
	s := h.next(t)
	if !s.Connected || s.Endpoint != "unix:///fake.sock" || names(s) != "web.test " {
		t.Fatalf("connected state %+v", s)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 4 {
		t.Errorf("attempts = %d", attempts)
	}
}

func TestWatcherFollowsEvents(t *testing.T) {
	fake := newFake(ctr("web", nil, pub(8080, 80)))
	h := run(t, func(context.Context) (Client, string, error) { return fake, "fake", nil })
	if s := h.next(t); names(s) != "web.test " {
		t.Fatalf("initial %+v", s)
	}

	// A burst of events leads to one re-list.
	before := fake.listCount()
	fake.set(ctr("web", nil, pub(8080, 80)), ctr("api", nil, pub(3000, 3000)))
	for range 5 {
		fake.events <- Event{Action: "start"}
	}
	if s := h.next(t); names(s) != "api.test web.test " {
		t.Fatalf("after start %+v", s)
	}
	time.Sleep(60 * time.Millisecond)
	if got := fake.listCount() - before; got != 1 {
		t.Errorf("burst caused %d lists, want 1", got)
	}

	// An event that changes nothing reports nothing.
	fake.events <- Event{Action: "update"}
	time.Sleep(80 * time.Millisecond)
	select {
	case s := <-h.states:
		t.Errorf("unchanged containers reported %+v", s)
	default:
	}

	fake.set()
	fake.events <- Event{Action: "die"}
	if s := h.next(t); !s.Connected || len(s.Routes) != 0 {
		t.Fatalf("after die %+v", s)
	}
}

func TestWatcherReconnects(t *testing.T) {
	var mu sync.Mutex
	var current *fakeDocker
	connect := func(context.Context) (Client, string, error) {
		mu.Lock()
		defer mu.Unlock()
		current = newFake(ctr("web", nil, pub(8080, 80)))
		return current, "fake", nil
	}
	h := run(t, connect)
	if s := h.next(t); !s.Connected {
		t.Fatalf("initial %+v", s)
	}
	mu.Lock()
	current.errc <- errors.New("connection reset")
	close(current.events)
	mu.Unlock()
	if s := h.next(t); s.Connected || len(s.Routes) != 0 || s.Err != "connection reset" {
		t.Fatalf("after failure %+v; Docker routes must be dropped", s)
	}
	if s := h.next(t); !s.Connected || names(s) != "web.test " {
		t.Fatalf("after reconnect %+v", s)
	}
}

func TestWatcherStopsOnCancel(t *testing.T) {
	h := run(t, func(context.Context) (Client, string, error) { return nil, "", errors.New("down") })
	h.next(t)
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("Run kept going after cancel")
	}
}
