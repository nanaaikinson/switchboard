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

// Route sources.
const (
	SourceConfig = "config" // routes.toml, managed with sb add and sb rm
	SourceDocker = "docker" // a running container; never persisted
)

// RouteStatus is a route plus its upstream health and where it comes from.
type RouteStatus struct {
	config.Route
	Health    Health `json:"health"`
	Source    string `json:"source"`              // SourceConfig or SourceDocker
	Container string `json:"container,omitempty"` // for SourceDocker
}

// DockerRoute is a route for a running container.
type DockerRoute struct {
	config.Route
	Container string
}

// DockerSkip is a container that publishes ports but has no route, and why.
type DockerSkip struct {
	Container string `json:"container"`
	Reason    string `json:"reason"`
}

// DockerStatus reports container discovery.
type DockerStatus struct {
	Enabled   bool         `json:"enabled"`
	Connected bool         `json:"connected"`
	Endpoint  string       `json:"endpoint,omitempty"`
	Error     string       `json:"error,omitempty"` // why it isn't connected; retried quietly
	Skipped   []DockerSkip `json:"skipped,omitempty"`
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
	Proxy         Listener      `json:"proxy"` // plain HTTP
	HTTPS         Listener      `json:"https"`
	Docker        DockerStatus  `json:"docker"`
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
