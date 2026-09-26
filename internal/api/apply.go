package api

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// Apply makes req.Routes the complete set of routes from req.File. Routes
// from that file which are no longer listed are removed. A route whose name
// or wildcard another source already has (sb add, another file, a Docker
// container) is not applied, and is reported as a conflict instead. Applying
// the same file twice changes nothing.
func (s *Service) Apply(req ApplyRequest) (ApplyResult, error) {
	if !filepath.IsAbs(req.File) {
		return ApplyResult{}, fmt.Errorf("%w: file %q must be an absolute path", ErrInvalid, req.File)
	}
	want := make([]config.Route, 0, len(req.Routes))
	for _, r := range req.Routes {
		if err := s.checkLocal(r.Name); err != nil {
			return ApplyResult{}, err
		}
		r.Name = s.qualify(r.Name)
		r.File = req.File
		if !config.ValidHostname(r.Name) {
			return ApplyResult{}, fmt.Errorf("%w: name %q must be a hostname like myapp or *.myapp", ErrInvalid, r.Name)
		}
		if err := checkPort(r.Port); err != nil {
			return ApplyResult{}, fmt.Errorf("%w: %s: %w", ErrInvalid, r.Name, err)
		}
		if slices.ContainsFunc(want, func(x config.Route) bool { return x.Name == r.Name }) {
			return ApplyResult{}, fmt.Errorf("%w: %s is listed twice in %s", ErrInvalid, r.Name, req.File)
		}
		want = append(want, r)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var res ApplyResult
	var next, mine []config.Route
	owners := map[string]string{} // claim -> who has it
	for _, r := range s.routes {
		if r.File == req.File {
			mine = append(mine, r)
			continue
		}
		next = append(next, r)
		for _, c := range claims(r) {
			owners[c] = ownerOf(r)
		}
	}
	for _, d := range s.active {
		for _, c := range claims(d.Route) {
			if owners[c] == "" {
				owners[c] = "Docker container " + d.Container
			}
		}
	}
	for _, r := range want {
		var owner string
		for _, c := range claims(r) {
			if owners[c] != "" {
				owner = owners[c]
			}
		}
		if s.reservedBy(r.Name) != "" {
			owner = "the Switchboard dashboard"
		}
		if owner != "" {
			res.Conflicts = append(res.Conflicts, Conflict{Name: r.Name, Owner: owner})
			continue
		}
		for _, c := range claims(r) {
			owners[c] = "an earlier route in " + req.File // a wildcard and an exact route can't share
		}
		next = append(next, r)
		switch i := slices.IndexFunc(mine, func(x config.Route) bool { return x.Name == r.Name }); {
		case i < 0:
			res.Added = append(res.Added, r)
		case mine[i] != r:
			res.Updated = append(res.Updated, r)
		default:
			res.Unchanged = append(res.Unchanged, r)
		}
	}
	for _, r := range mine {
		if !slices.ContainsFunc(next, func(x config.Route) bool { return x.Name == r.Name && x.File == req.File }) {
			res.Removed = append(res.Removed, r)
		}
	}
	if len(res.Added)+len(res.Updated)+len(res.Removed) == 0 {
		return res, nil // idempotent: nothing to save, no events
	}
	if err := s.commit(next); err != nil {
		return ApplyResult{}, err
	}
	for _, r := range res.Removed {
		s.hub.publish(Event{Type: EventRouteRemoved, Route: RouteStatus{Route: r, Health: HealthUnknown, Source: SourceFile}})
	}
	for _, r := range res.Added {
		s.hub.publish(Event{Type: EventRouteAdded, Route: s.withHealth(r)})
	}
	for _, r := range res.Updated {
		s.hub.publish(Event{Type: EventRouteUpdated, Route: s.withHealth(r)})
	}
	return res, nil
}

// ownerOf describes where a config route came from, for conflict messages.
func ownerOf(r config.Route) string {
	if r.File != "" {
		return "a route from " + r.File
	}
	return "a route added with 'sb add'"
}
