package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
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
	ConfigPath     string         // routes.toml; every change is saved here
	Config         *config.Config // loaded config; routes are served immediately
	Proxy          RouteSetter
	TLDs           []string // first entry is appended to unqualified names
	Version        string
	HealthInterval time.Duration // 0 means DefaultHealthInterval
}

// Service owns the route table: it validates changes, persists them, pushes
// them to the proxy, tracks upstream health and publishes events.
type Service struct {
	opts    Options
	started time.Time
	hub     *hub
	health  *checker

	mu           sync.Mutex     // serializes route changes
	routes       []config.Route // from routes.toml
	docker       []DockerRoute  // from running containers
	active       []DockerRoute  // the Docker routes served: those not clashing with routes
	conflicts    []DockerSkip   // the others, and why
	dockerStatus DockerStatus

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
	s := &Service{opts: opts, started: time.Now(), hub: newHub(), routes: slices.Clone(opts.Config.Routes)}
	s.health = newChecker(opts.HealthInterval, s.healthChanged)
	if err := opts.Proxy.SetRoutes(s.routes); err != nil {
		return nil, fmt.Errorf("api: load routes: %w", err)
	}
	s.health.track(ports(s.routes))
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
		out = append(out, dockerStatus(d, s.health.health(d.Port)))
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
	s.mu.Unlock()
	return Status{
		Version:       s.opts.Version,
		UptimeSeconds: int64(time.Since(s.started).Seconds()),
		TLDs:          slices.Clone(s.opts.TLDs),
		DNS:           dns,
		Proxy:         proxy,
		HTTPS:         https,
		Docker:        docker,
		Routes:        s.Routes(),
	}
}

// Put adds r, or replaces the route with the same name. The name is qualified
// with the default TLD first. It reports whether the route is new.
func (s *Service) Put(r config.Route) (RouteStatus, bool, error) {
	r.Name = config.QualifyName(r.Name, s.opts.TLDs)
	if !config.ValidHostname(r.Name) {
		return RouteStatus{}, false, fmt.Errorf("%w: name %q must be a hostname like myapp or *.myapp", ErrInvalid, r.Name)
	}
	if r.Port < 1 || r.Port > 65535 {
		return RouteStatus{}, false, fmt.Errorf("%w: port %d out of range 1-65535", ErrInvalid, r.Port)
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
	name = config.QualifyName(name, s.opts.TLDs)
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
	s.hub.publish(Event{Type: EventRouteRemoved, Route: RouteStatus{Route: gone, Health: HealthUnknown, Source: SourceConfig}})
	return gone, nil
}

// commit applies next, with the Docker routes that don't clash with it, to
// the proxy, then saves next; on save failure the proxy is rolled back so
// memory, proxy and disk never disagree. Caller holds s.mu.
func (s *Service) commit(next []config.Route) error {
	all, active, conflicts := merge(next, s.docker)
	if err := s.opts.Proxy.SetRoutes(all); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	cfg := &config.Config{SchemaVersion: config.SchemaVersion, Routes: next}
	if err := config.Save(s.opts.ConfigPath, cfg); err != nil {
		prev, _, _ := merge(s.routes, s.docker)
		if rerr := s.opts.Proxy.SetRoutes(prev); rerr != nil {
			return fmt.Errorf("%w (and restoring the previous routes failed: %w)", err, rerr)
		}
		return err
	}
	old := s.active
	s.routes, s.active, s.conflicts = next, active, conflicts
	s.health.track(ports(all))
	s.publishDockerDiff(old, active)
	return nil
}

func (s *Service) withHealth(r config.Route) RouteStatus {
	return RouteStatus{Route: r, Health: s.health.health(r.Port), Source: SourceConfig}
}

// healthChanged publishes one event per route on the port.
func (s *Service) healthChanged(port int, h Health) {
	s.mu.Lock()
	var affected []RouteStatus
	for _, r := range s.routes {
		if r.Port == port {
			affected = append(affected, RouteStatus{Route: r, Health: h, Source: SourceConfig})
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
