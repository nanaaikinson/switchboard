package api

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// ErrConflict is returned for changes that clash with routes the Service
// doesn't own, such as removing a Docker route.
var ErrConflict = errors.New("conflict")

// SetDocker replaces the Docker routes and discovery status. Docker routes are
// served next to the config routes but never saved; a config route with the
// same name wins.
func (s *Service) SetDocker(st DockerStatus, routes []DockerRoute) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, active, conflicts := s.merge(s.routes, routes)
	if err := s.opts.Proxy.SetRoutes(all); err != nil {
		// Never let container routes break the config routes.
		slog.Warn("docker routes rejected", "err", err)
		for _, r := range routes {
			conflicts = append(conflicts, DockerSkip{Container: r.Container, Reason: err.Error()})
		}
		routes, active = nil, nil
		all, _, _ = s.merge(s.routes, nil)
		if err := s.opts.Proxy.SetRoutes(all); err != nil {
			slog.Error("restore config routes", "err", err)
		}
	}
	old := s.active
	s.docker, s.active, s.conflicts, s.dockerStatus = routes, active, conflicts, st
	s.health.track(ports(all))
	s.publishDockerDiff(old, active)
	s.announce()
}

// merge returns the config routes plus the Docker routes that don't claim a
// reserved name, or a name or wildcard a config route (or an earlier Docker
// route) already has. Config routes with reserved names are left out.
func (s *Service) merge(cfg []config.Route, docker []DockerRoute) (all []config.Route, active []DockerRoute, conflicts []DockerSkip) {
	used := map[string]string{}
	for _, r := range cfg {
		if s.reservedBy(r.Name) != "" {
			continue // kept in routes.toml from an older version, never served
		}
		all = append(all, r)
		for _, c := range claims(r) {
			used[c] = "a route in routes.toml"
		}
	}
	for _, d := range docker {
		var owner string
		for _, c := range claims(d.Route) {
			if used[c] != "" {
				owner = used[c]
			}
		}
		if s.reservedBy(d.Name) != "" {
			owner = "the Switchboard dashboard"
		}
		if owner != "" {
			conflicts = append(conflicts, DockerSkip{Container: d.Container, Reason: fmt.Sprintf("%s is taken by %s", d.Name, owner)})
			continue
		}
		for _, c := range claims(d.Route) {
			used[c] = "container " + d.Container
		}
		all = append(all, d.Route)
		active = append(active, d)
	}
	return all, active, conflicts
}

// claims are what a route answers for: its exact name and/or "*.<base>".
func claims(r config.Route) []string {
	base, star := strings.CutPrefix(strings.ToLower(r.Name), "*.")
	var out []string
	if !star {
		out = append(out, base)
	}
	if star || r.Wildcard {
		out = append(out, "*."+base)
	}
	return out
}

// publishDockerDiff sends route events for Docker routes that appeared,
// changed or went away. Caller holds s.mu.
func (s *Service) publishDockerDiff(before, after []DockerRoute) {
	find := func(list []DockerRoute, name string) (DockerRoute, bool) {
		i := slices.IndexFunc(list, func(d DockerRoute) bool { return d.Name == name })
		if i < 0 {
			return DockerRoute{}, false
		}
		return list[i], true
	}
	for _, d := range before {
		if _, ok := find(after, d.Name); !ok {
			s.hub.publish(Event{Type: EventRouteRemoved, Route: dockerStatus(d, HealthUnknown)})
		}
	}
	for _, d := range after {
		switch old, ok := find(before, d.Name); {
		case !ok:
			s.hub.publish(Event{Type: EventRouteAdded, Route: dockerStatus(d, s.health.health(d.Port))})
		case old != d:
			s.hub.publish(Event{Type: EventRouteUpdated, Route: dockerStatus(d, s.health.health(d.Port))})
		}
	}
}

func dockerStatus(d DockerRoute, h Health) RouteStatus {
	return RouteStatus{Route: d.Route, Health: h, Source: SourceDocker, Container: d.Container}
}
