// Package mdns announces routes under .local over multicast DNS, each name
// pointing at 127.0.0.1 and ::1. It is experimental and opt-in: the daemon
// only runs it after 'sb tld add local --mdns'.
//
// Names are announced on the loopback interface only, so other machines on
// the network never learn them. The OS's own responder is preferred where the
// platform provides one; the Go responder in this package is the fallback.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// TTL is the lifetime of announced records, in seconds. It is short so that
// names outlive a crashed daemon by seconds, not minutes.
const TTL = 10

// DefaultRetry is how often a failed announcement is retried.
const DefaultRetry = 10 * time.Second

// Publisher announces host records on one backend.
type Publisher interface {
	// Set replaces the announced names (lowercase, no trailing dot): new
	// names are announced, names no longer listed are withdrawn.
	Set(names []string) error
	// Close withdraws every name and releases the backend.
	Close() error
}

// Backend is one way to announce names, such as the OS's responder.
type Backend struct {
	Name      string // shown in status, e.g. "go"
	Interface string // where it announces, e.g. "loopback"
	Open      func() (Publisher, error)
}

// State reports what the announcer is doing.
type State struct {
	Backend   string   // the backend in use; empty if none could start
	Interface string   // where names are announced
	Announced []string // sorted
	Error     string   // why names are not announced; retried
}

// Announcer keeps the announced names in line with the route table. Update
// never blocks; Run does the work.
type Announcer struct {
	backends []Backend
	// OnChange, if set, is called from Run after the state changes.
	OnChange func(State)
	// Retry is how long to wait after a failure; 0 means DefaultRetry.
	Retry time.Duration

	mu      sync.Mutex
	enabled bool
	want    []string
	state   State
	wake    chan struct{} // capacity 1
}

// NewAnnouncer returns an announcer that uses the first backend that opens.
func NewAnnouncer(backends ...Backend) *Announcer {
	return &Announcer{backends: backends, wake: make(chan struct{}, 1)}
}

// Update sets whether mDNS is enabled and the names to announce.
func (a *Announcer) Update(enabled bool, names []string) {
	want := normalize(names)
	a.mu.Lock()
	a.enabled, a.want = enabled, want
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// State returns the current state.
func (a *Announcer) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.state
	st.Announced = slices.Clone(st.Announced)
	return st
}

// Run announces names until ctx is done, then withdraws them.
func (a *Announcer) Run(ctx context.Context) {
	retry := a.Retry
	if retry <= 0 {
		retry = DefaultRetry
	}
	var (
		pub     Publisher
		backend Backend
	)
	closePub := func() {
		if pub != nil {
			if err := pub.Close(); err != nil {
				slog.Debug("mdns: close backend", "backend", backend.Name, "err", err)
			}
			pub = nil
		}
	}
	defer closePub()

	for {
		a.mu.Lock()
		enabled, want := a.enabled, a.want
		a.mu.Unlock()

		var st State
		var err error
		if enabled {
			if pub == nil {
				pub, backend, err = a.open()
			}
			if err == nil {
				if err = pub.Set(want); err != nil {
					err = fmt.Errorf("%s: %w", backend.Name, err)
					closePub() // reopen on retry; the backend may have restarted
				}
			}
			st = State{Backend: backend.Name, Interface: backend.Interface}
			if err != nil {
				st.Error = err.Error()
			} else {
				st.Announced = want
			}
		} else {
			closePub()
		}
		a.setState(st)

		var timer <-chan time.Time
		if err != nil {
			slog.Warn("mdns: names not announced; retrying", "err", err, "retry", retry)
			timer = time.After(retry)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-timer:
		}
	}
}

// open tries each backend in turn.
func (a *Announcer) open() (Publisher, Backend, error) {
	if len(a.backends) == 0 {
		return nil, Backend{}, errors.New("no mDNS responder is available on this system")
	}
	var errs []string
	for _, b := range a.backends {
		p, err := b.Open()
		if err == nil {
			slog.Info("mdns: announcing .local names (experimental)", "backend", b.Name, "interface", b.Interface)
			return p, b, nil
		}
		slog.Debug("mdns: backend unavailable", "backend", b.Name, "err", err)
		errs = append(errs, b.Name+": "+err.Error())
	}
	return nil, Backend{}, errors.New(strings.Join(errs, "; "))
}

func (a *Announcer) setState(st State) {
	a.mu.Lock()
	changed := !equalState(a.state, st)
	a.state = st
	a.mu.Unlock()
	if changed && a.OnChange != nil {
		a.OnChange(st)
	}
}

func equalState(x, y State) bool {
	return x.Backend == y.Backend && x.Interface == y.Interface && x.Error == y.Error && slices.Equal(x.Announced, y.Announced)
}

// normalize lowercases names, strips trailing dots, drops duplicates and
// sorts them.
func normalize(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSuffix(strings.ToLower(n), ".")
		if n != "" {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
