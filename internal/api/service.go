package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/mdns"
	"github.com/nanaaikinson/switchboard/internal/proxy"
)

// ErrNotFound is returned when a route does not exist.
var ErrNotFound = errors.New("route not found")

// ErrInvalid wraps errors caused by bad client input.
var ErrInvalid = errors.New("invalid route")

// RouteSetter receives the route table whenever it changes. *proxy.ReverseProxy
// satisfies it.
type RouteSetter interface {
	SetRoutes([]config.Route) error
}

// Options configures a Service.
type Options struct {
	ConfigPath string         // routes.toml; every change is saved here
	Config     *config.Config // loaded config; routes are served immediately
	Proxy      RouteSetter
	// TLDs are served through split DNS; the first entry is appended to
	// unqualified names. Opt-in mDNS TLDs come from Config.TLDs.
	TLDs           []string
	Version        string
	HealthInterval time.Duration // 0 means DefaultHealthInterval
	// Reserved names, and every name under them, can't be routes, e.g. the
	// dashboard's switchboard.<tld> and *.switchboard.<tld>.
	Reserved []string
	// Logs returns a route's recent requests; nil means none are kept.
	Logs func(name string) []proxy.AccessLog
	// CA reports the local CA and whether the system trusts it; nil reports
	// no CA.
	CA func() CAInfo
	// Pause turns every route off (true) or back on; nil can't pause.
	Pause func(paused bool)
	// MDNS announces routes under mDNS TLDs; nil means they can't be.
	MDNS Announcer
}

// Announcer announces names over mDNS. *mdns.Announcer satisfies it.
type Announcer interface {
	Update(enabled bool, names []string)
	State() mdns.State
}

// Service owns the route table: it validates changes, persists them, pushes
// them to the proxy, tracks upstream health and publishes events.
type Service struct {
	opts    Options
	started time.Time
	hub     *hub
	health  *checker

	mu           sync.Mutex     // serializes route changes
	tlds         []config.TLD   // opt-in TLDs from routes.toml
	routes       []config.Route // from routes.toml
	docker       []DockerRoute  // from running containers
	active       []DockerRoute  // the Docker routes served: those not clashing with routes
	conflicts    []DockerSkip   // the others, and why
	dockerStatus DockerStatus
	paused       bool

	lmu   sync.Mutex
	dns   Listener
	proxy Listener
	https Listener
}

// NewService serves opts.Config's routes through opts.Proxy.
func NewService(opts Options) (*Service, error) {
	if opts.HealthInterval <= 0 {
		opts.HealthInterval = DefaultHealthInterval
	}
	if len(opts.TLDs) == 0 {
		return nil, errors.New("api: at least one TLD is required")
	}
	s := &Service{opts: opts, started: time.Now(), hub: newHub(),
		tlds: slices.Clone(opts.Config.TLDs), routes: slices.Clone(opts.Config.Routes)}
	s.health = newChecker(opts.HealthInterval, s.healthChanged)
	if n := count(s.routes, func(r config.Route) bool { return s.reservedBy(r.Name) != "" }); n > 0 {
		// Older versions only reserved the dashboard's exact name. Keep such
		// routes in routes.toml, so nothing is lost, but never serve them.
		slog.Warn("routes.toml has routes under the dashboard's reserved name; they are not served. Find them with 'sb ls' and remove them with 'sb rm'",
			"count", n)
	}
	if n := count(s.routes, func(r config.Route) bool { return checkPort(r.Port) != nil }); n > 0 {
		// Older versions accepted these; the proxy answers them with 508.
		slog.Warn("routes.toml has routes to port 80 or 443, where Switchboard itself listens; they answer 508 Loop Detected. Point them at your app's port with 'sb add <name> <port>'",
			"count", n)
	}
	all, _, _ := s.merge(s.routes, nil)
	if err := opts.Proxy.SetRoutes(all); err != nil {
		return nil, fmt.Errorf("api: load routes: %w", err)
	}
	s.health.track(ports(all))
	s.announce()
	return s, nil
}

// Run checks upstream health until ctx is done.
func (s *Service) Run(ctx context.Context) { s.health.run(ctx) }

// SetDNS records the DNS server's listening state for GET /status.
func (s *Service) SetDNS(l Listener) { s.lmu.Lock(); s.dns = l; s.lmu.Unlock() }

// SetProxy records the HTTP proxy's listening state for GET /status.
func (s *Service) SetProxy(l Listener) { s.lmu.Lock(); s.proxy = l; s.lmu.Unlock() }

// SetHTTPS records the HTTPS proxy's listening state for GET /status.
func (s *Service) SetHTTPS(l Listener) { s.lmu.Lock(); s.https = l; s.lmu.Unlock() }

// Routes returns every route with its health: config routes, then Docker
// routes.
func (s *Service) Routes() []RouteStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RouteStatus, 0, len(s.routes)+len(s.active))
	for _, r := range s.routes {
		out = append(out, s.withHealth(r))
	}
	for _, d := range s.active {
		rs := dockerStatus(d, s.health.health(d.Port))
		rs.MDNS = s.routeMDNS(d.Name)
		out = append(out, rs)
	}
	return out
}

// Status reports version, uptime, component state and routes.
func (s *Service) Status() Status {
	s.lmu.Lock()
	dns, proxy, https := s.dns, s.proxy, s.https
	s.lmu.Unlock()
	s.mu.Lock()
	docker := s.dockerStatus
	docker.Skipped = slices.Concat(docker.Skipped, s.conflicts)
	paused := s.paused
	tlds, mdnsStatus := s.tldNames(), s.mdnsStatus()
	s.mu.Unlock()
	return Status{
		Version:       s.opts.Version,
		UptimeSeconds: int64(time.Since(s.started).Seconds()),
		TLDs:          tlds,
		MDNS:          mdnsStatus,
		DNS:           dns,
		Proxy:         proxy,
		HTTPS:         https,
		Docker:        docker,
		Paused:        paused,
		Routes:        s.Routes(),
	}
}

// Put adds r, or replaces the route with the same name. The name is qualified
// with the default TLD first. It reports whether the route is new.
func (s *Service) Put(r config.Route) (RouteStatus, bool, error) {
	if err := s.checkLocal(r.Name); err != nil {
		return RouteStatus{}, false, err
	}
	r.Name = s.qualify(r.Name)
	r.File = "" // routes from files are managed with Apply
	if !config.ValidHostname(r.Name) {
		return RouteStatus{}, false, fmt.Errorf("%w: name %q must be a hostname like myapp or *.myapp", ErrInvalid, r.Name)
	}
	if err := s.checkReserved(r); err != nil {
		return RouteStatus{}, false, err
	}
	if err := checkPort(r.Port); err != nil {
		return RouteStatus{}, false, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := slices.Clone(s.routes)
	i := slices.IndexFunc(next, func(x config.Route) bool { return x.Name == r.Name })
	created := i < 0
	if created {
		next = append(next, r)
	} else {
		next[i] = r
	}
	if err := s.commit(next); err != nil {
		return RouteStatus{}, false, err
	}
	typ := EventRouteUpdated
	if created {
		typ = EventRouteAdded
	}
	rs := s.withHealth(r)
	s.hub.publish(Event{Type: typ, Route: rs})
	return rs, created, nil
}

// Delete removes the route called name (qualified with the default TLD) and
// returns it.
func (s *Service) Delete(name string) (config.Route, error) {
	name = s.qualify(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.routes, func(x config.Route) bool { return x.Name == name })
	if i < 0 {
		if j := slices.IndexFunc(s.active, func(d DockerRoute) bool { return d.Name == name }); j >= 0 {
			return config.Route{}, fmt.Errorf("%w: %s comes from Docker container %s; stop the container, or label it dev.switchboard.enable=false",
				ErrConflict, name, s.active[j].Container)
		}
		return config.Route{}, fmt.Errorf("%w: %s; list routes with 'sb ls'", ErrNotFound, name)
	}
	gone := s.routes[i]
	if err := s.commit(slices.Delete(slices.Clone(s.routes), i, i+1)); err != nil {
		return config.Route{}, err
	}
	removed := RouteStatus{Route: gone, Health: HealthUnknown, Source: SourceConfig}
	if gone.File != "" {
		removed.Source = SourceFile
	}
	s.hub.publish(Event{Type: EventRouteRemoved, Route: removed})
	return gone, nil
}

// commit applies next, with the Docker routes that don't clash with it, to
// the proxy, then saves next; on save failure the proxy is rolled back so
// memory, proxy and disk never disagree. Caller holds s.mu.
func (s *Service) commit(next []config.Route) error {
	all, active, conflicts := s.merge(next, s.docker)
	if err := s.opts.Proxy.SetRoutes(all); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := s.save(s.tlds, next); err != nil {
		prev, _, _ := s.merge(s.routes, s.docker)
		if rerr := s.opts.Proxy.SetRoutes(prev); rerr != nil {
			return fmt.Errorf("%w (and restoring the previous routes failed: %w)", err, rerr)
		}
		return err
	}
	old := s.active
	s.routes, s.active, s.conflicts = next, active, conflicts
	s.health.track(ports(all))
	s.publishDockerDiff(old, active)
	s.announce()
	return nil
}

// save writes tlds and routes to routes.toml.
func (s *Service) save(tlds []config.TLD, routes []config.Route) error {
	return config.Save(s.opts.ConfigPath, &config.Config{SchemaVersion: config.SchemaVersion, TLDs: tlds, Routes: routes})
}

// withHealth adds r's health, source and mDNS state. Caller holds s.mu.
func (s *Service) withHealth(r config.Route) RouteStatus {
	src := SourceConfig
	if r.File != "" {
		src = SourceFile
	}
	return RouteStatus{Route: r, Health: s.health.health(r.Port), Source: src, MDNS: s.routeMDNS(r.Name)}
}

// healthChanged publishes one event per route on the port.
func (s *Service) healthChanged(port int, h Health) {
	s.mu.Lock()
	var affected []RouteStatus
	for _, r := range s.routes {
		if r.Port == port {
			rs := s.withHealth(r)
			rs.Health = h
			affected = append(affected, rs)
		}
	}
	for _, d := range s.active {
		if d.Port == port {
			affected = append(affected, dockerStatus(d, h))
		}
	}
	s.mu.Unlock()
	for _, r := range affected {
		s.hub.publish(Event{Type: EventHealthChanged, Route: r})
	}
}

func ports(routes []config.Route) []int {
	out := make([]int, 0, len(routes))
	for _, r := range routes {
		if !slices.Contains(out, r.Port) {
			out = append(out, r.Port)
		}
	}
	return out
}

// checkPort rejects ports outside 1-65535, and the proxy's own default ports:
// a route to one sends every request back to the proxy. The proxy detects
// such loops itself too, since --http-addr can move it to other ports.
func checkPort(port int) error {
	switch {
	case port < 1 || port > 65535:
		return fmt.Errorf("port %d out of range 1-65535", port)
	case port == 80 || port == 443:
		return fmt.Errorf("port %d is where Switchboard itself listens, so the route would loop back to it; use your app's port, like 3000", port)
	}
	return nil
}

// checkReserved rejects routes for names Switchboard itself serves.
func (s *Service) checkReserved(r config.Route) error {
	if root := s.reservedBy(r.Name); root != "" {
		return fmt.Errorf("%w: %s is reserved: %s and every name under it belong to the Switchboard dashboard; pick another name",
			ErrInvalid, r.Name, root)
	}
	return nil
}

// reservedBy returns the reserved name that name (or the base of a "*.name"
// wildcard) is, or is under; "" if none. The dashboard owns its whole subtree
// so that no route can serve a page next to it.
func (s *Service) reservedBy(name string) string {
	base := strings.TrimPrefix(strings.ToLower(name), "*.")
	for _, n := range s.opts.Reserved {
		if base == n || under(base, n) {
			return n
		}
	}
	return ""
}

// count counts the routes for which f is true.
func count(routes []config.Route, f func(config.Route) bool) int {
	n := 0
	for _, r := range routes {
		if f(r) {
			n++
		}
	}
	return n
}

// Logs returns the recent requests to the route called name (qualified with
// the default TLD), oldest first.
func (s *Service) Logs(name string) ([]proxy.AccessLog, error) {
	name = s.qualify(name)
	s.mu.Lock()
	known := slices.ContainsFunc(s.routes, func(r config.Route) bool { return r.Name == name }) ||
		slices.ContainsFunc(s.active, func(d DockerRoute) bool { return d.Name == name })
	s.mu.Unlock()
	if !known {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if s.opts.Logs == nil {
		return []proxy.AccessLog{}, nil
	}
	return s.opts.Logs(name), nil
}

// CA reports the local CA.
func (s *Service) CA() CAInfo {
	if s.opts.CA == nil {
		return CAInfo{}
	}
	return s.opts.CA()
}

// SetPaused turns every route off or back on, until the daemon restarts, and
// publishes a paused.changed event.
func (s *Service) SetPaused(paused bool) error {
	if s.opts.Pause == nil {
		return fmt.Errorf("%w: this daemon can't pause", ErrInvalid)
	}
	s.mu.Lock()
	changed := s.paused != paused
	s.paused = paused
	s.opts.Pause(paused)
	s.mu.Unlock()
	if changed {
		s.hub.publish(Event{Type: EventPausedChanged, Paused: &paused})
	}
	return nil
}
