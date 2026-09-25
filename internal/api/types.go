package api

import "github.com/nanaaikinson/switchboard/internal/config"

// Health is the reachability of a route's upstream port.
type Health string

// Health values.
const (
	HealthUnknown Health = "unknown"
	HealthUp      Health = "up"
	HealthDown    Health = "down"
)

// RouteStatus is a route plus its upstream health.
type RouteStatus struct {
	config.Route
	Health Health `json:"health"`
}

// Listener reports whether a daemon component is serving.
type Listener struct {
	Addrs     []string `json:"addrs"`
	Listening bool     `json:"listening"`
	Error     string   `json:"error,omitempty"`
}

// Status is the response of GET /v1/status.
type Status struct {
	Version       string        `json:"version"`
	UptimeSeconds int64         `json:"uptime_seconds"`
	TLDs          []string      `json:"tlds"`
	DNS           Listener      `json:"dns"`
	Proxy         Listener      `json:"proxy"`
	Routes        []RouteStatus `json:"routes"`
}

// Event types sent on GET /v1/events.
const (
	EventRouteAdded    = "route.added"
	EventRouteUpdated  = "route.updated"
	EventRouteRemoved  = "route.removed"
	EventHealthChanged = "health.changed"
)

// Event is one server-sent event.
type Event struct {
	Type  string      `json:"type"`
	Route RouteStatus `json:"route"`
}

// Error is the body of every non-2xx response.
type Error struct {
	Error string `json:"error"`
}
