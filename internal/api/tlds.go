package api

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// TLDs lists the served TLDs, default first.
func (s *Service) TLDs() []TLD {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TLD, 0, len(s.opts.TLDs)+len(s.tlds))
	for i, t := range s.opts.TLDs {
		out = append(out, TLD{Name: t, Default: i == 0})
	}
	for _, t := range s.tlds {
		out = append(out, TLD{Name: t.Name, MDNS: t.MDNS})
	}
	return out
}

// AddTLD adds an opt-in TLD and reports whether it is new. Only .local over
// mDNS is supported: other TLDs need split DNS, which 'sb setup' installs.
func (s *Service) AddTLD(name string, mdns bool) (bool, error) {
	name = strings.Trim(strings.ToLower(strings.TrimSpace(name)), ".")
	switch {
	case name == config.MDNSTLD && !mdns:
		return false, fmt.Errorf("%w: .local is reserved for multicast DNS; add it with: sb tld add local --mdns", ErrInvalid)
	case slices.Contains(s.opts.TLDs, name) && mdns:
		return false, fmt.Errorf("%w: .%s is served through split DNS; mDNS only works for .local", ErrInvalid, name)
	case slices.Contains(s.opts.TLDs, name):
		return false, nil
	case name != config.MDNSTLD && mdns:
		return false, fmt.Errorf("%w: mDNS only works for .local; use: sb tld add local --mdns", ErrInvalid)
	case name != config.MDNSTLD:
		return false, fmt.Errorf("%w: only .local (with --mdns) can be added for now; other TLDs need split DNS, which sb tld does not set up yet", ErrInvalid)
	case s.opts.MDNS == nil:
		return false, fmt.Errorf("%w: this daemon can't announce names over mDNS", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.tlds, func(t config.TLD) bool { return t.Name == name }) {
		return false, nil
	}
	next := append(slices.Clone(s.tlds), config.TLD{Name: name, MDNS: true})
	if err := s.save(next, s.routes); err != nil {
		return false, err
	}
	s.tlds = next
	s.announce()
	s.hub.publish(Event{Type: EventTLDsChanged})
	return true, nil
}

// RemoveTLD removes an opt-in TLD. Routes under it must be removed first.
func (s *Service) RemoveTLD(name string) error {
	name = strings.Trim(strings.ToLower(strings.TrimSpace(name)), ".")
	if slices.Contains(s.opts.TLDs, name) {
		return fmt.Errorf("%w: .%s is set up by 'sb setup'; remove it with 'sb uninstall'", ErrInvalid, name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.tlds, func(t config.TLD) bool { return t.Name == name })
	if i < 0 {
		return fmt.Errorf("%w: .%s is not configured; list TLDs with 'sb tld ls'", ErrNotFound, name)
	}
	var using []string
	for _, r := range s.routes {
		if under(r.Name, name) {
			using = append(using, r.Name)
		}
	}
	for _, d := range s.active {
		if under(d.Name, name) {
			using = append(using, d.Name)
		}
	}
	if len(using) > 0 {
		return fmt.Errorf("%w: routes still use .%s (%s); remove them first, e.g. sb rm %s",
			ErrConflict, name, strings.Join(using, ", "), using[0])
	}
	next := slices.Delete(slices.Clone(s.tlds), i, i+1)
	if err := s.save(next, s.routes); err != nil {
		return err
	}
	s.tlds = next
	s.announce()
	s.hub.publish(Event{Type: EventTLDsChanged})
	return nil
}

// MDNSChanged tells clients that the announcer's state changed.
func (s *Service) MDNSChanged() { s.hub.publish(Event{Type: EventMDNSChanged}) }

// checkLocal rejects names under .local while .local mode is off; they
// would otherwise get the default TLD, as in myapp.local.test.
func (s *Service) checkLocal(name string) error {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if !under(n, config.MDNSTLD) {
		return nil
	}
	s.mu.Lock()
	on := slices.Contains(s.mdnsTLDs(), config.MDNSTLD)
	s.mu.Unlock()
	if on {
		return nil
	}
	return fmt.Errorf("%w: %s is under .local, which is off; turn on the experimental .local mode with 'sb tld add local --mdns', or drop .local", ErrInvalid, n)
}

// qualify appends the default TLD to name unless it has a served one.
func (s *Service) qualify(name string) string {
	s.mu.Lock()
	tlds := s.tldNames()
	s.mu.Unlock()
	return config.QualifyName(name, tlds)
}

// tldNames lists every served TLD, default first. Caller holds s.mu.
func (s *Service) tldNames() []string {
	out := slices.Clone(s.opts.TLDs)
	for _, t := range s.tlds {
		out = append(out, t.Name)
	}
	return out
}

// mdnsTLDs lists the TLDs resolved over mDNS. Caller holds s.mu.
func (s *Service) mdnsTLDs() []string {
	out := []string{}
	for _, t := range s.tlds {
		if t.MDNS {
			out = append(out, t.Name)
		}
	}
	return out
}

// announce hands every exact route name under an mDNS TLD to the announcer.
// Caller holds s.mu.
func (s *Service) announce() {
	if s.opts.MDNS == nil {
		return
	}
	tlds := s.mdnsTLDs()
	var names []string
	add := func(r config.Route) {
		if !strings.HasPrefix(r.Name, "*.") && slices.ContainsFunc(tlds, func(t string) bool { return under(r.Name, t) }) {
			names = append(names, r.Name)
		}
	}
	for _, r := range s.routes {
		add(r)
	}
	for _, d := range s.active {
		add(d.Route)
	}
	s.opts.MDNS.Update(len(tlds) > 0, names)
}

// routeMDNS is a route's mDNS state, or "" if it isn't under an mDNS TLD.
// Caller holds s.mu.
func (s *Service) routeMDNS(name string) string {
	if !slices.ContainsFunc(s.mdnsTLDs(), func(t string) bool { return under(name, t) }) {
		return ""
	}
	switch {
	case strings.HasPrefix(name, "*."):
		return MDNSWildcard
	case s.opts.MDNS != nil && slices.Contains(s.opts.MDNS.State().Announced, name):
		return MDNSAnnounced
	}
	return MDNSPending
}

// mdnsStatus reports the .local mode for GET /v1/status. Caller holds s.mu.
func (s *Service) mdnsStatus() MDNSStatus {
	st := MDNSStatus{Experimental: true, TLDs: s.mdnsTLDs()}
	st.Enabled = len(st.TLDs) > 0
	if st.Enabled && s.opts.MDNS != nil {
		a := s.opts.MDNS.State()
		st.Backend, st.Interface, st.Announced, st.Error = a.Backend, a.Interface, len(a.Announced), a.Error
	}
	return st
}

// under reports whether name is inside tld.
func under(name, tld string) bool { return strings.HasSuffix(name, "."+tld) }
