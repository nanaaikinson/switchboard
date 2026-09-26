package docker

import (
	"context"
	"log/slog"
	"reflect"
	"time"
)

// Defaults for Watcher.
const (
	DefaultRetry    = 10 * time.Second
	DefaultDebounce = 250 * time.Millisecond
)

// State is what the watcher knows about Docker.
type State struct {
	Connected bool
	Endpoint  string
	Err       string // why it is not connected
	Routes    []Route
	Skipped   []Skip
}

// Watcher keeps routes in sync with running containers. It reconnects
// quietly, every Retry, whenever Docker isn't reachable.
type Watcher struct {
	// Connect returns a client and a description of its endpoint.
	Connect  func(ctx context.Context) (Client, string, error)
	TLDs     []string
	OnChange func(State) // called with each new state, never concurrently
	Retry    time.Duration
	Debounce time.Duration // coalesces bursts of events
}

// Connect finds the Docker endpoint in the process environment.
func Connect(ctx context.Context) (Client, string, error) {
	ep, err := Find(ctx, Env{})
	if err != nil {
		return nil, "", err
	}
	return NewClient(ep), ep.String(), nil
}

// Run watches until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	retry, debounce := w.Retry, w.Debounce
	if retry <= 0 {
		retry = DefaultRetry
	}
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	var last *State
	report := func(s State) {
		if last == nil || !reflect.DeepEqual(*last, s) {
			last = &s
			w.OnChange(s)
		}
	}
	for ctx.Err() == nil {
		c, ep, err := w.Connect(ctx)
		if err == nil {
			// The endpoint and errors can hold a DOCKER_HOST name, so they
			// stay at debug; GET /v1/status and the dashboard show them.
			slog.Info("docker connected")
			slog.Debug("docker connected", "endpoint", ep)
			err = w.watch(ctx, c, ep, debounce, report)
			if ctx.Err() != nil {
				return
			}
			slog.Info("docker disconnected; retrying", "every", retry)
			slog.Debug("docker disconnected", "err", err)
		} else {
			slog.Debug("docker not reachable; retrying", "err", err, "every", retry)
		}
		report(State{Err: err.Error()}) // drops every Docker route
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

// watch subscribes to events, then lists containers, and lists them again
// after each burst of events. It returns when the connection fails.
func (w *Watcher) watch(ctx context.Context, c Client, ep string, debounce time.Duration, report func(State)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, errc := c.Events(ctx) // before listing, so no change is missed
	sync := func() error {
		cs, err := c.Containers(ctx)
		if err != nil {
			return err
		}
		routes, skipped := Routes(cs, w.TLDs)
		report(State{Connected: true, Endpoint: ep, Routes: routes, Skipped: skipped})
		return nil
	}
	if err := sync(); err != nil {
		return err
	}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case ev, ok := <-events:
			if !ok {
				events = nil // errc reports why
				continue
			}
			slog.Debug("docker event", "action", ev.Action)
			if settle == nil {
				settle = time.After(debounce)
			}
		case <-settle:
			settle = nil
			if err := sync(); err != nil {
				return err
			}
		}
	}
}
