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
	SourceConfig = "config" // added with sb add
	SourceFile   = "file"   // applied from the switchboard.toml named in the route's "file"
	SourceDocker = "docker" // a running container; never persisted
)

// RouteStatus is a route plus its upstream health and where it comes from.
type RouteStatus struct {
	config.Route
	Health    Health `json:"health"`
	Source    string `json:"source"`              // SourceConfig, SourceFile or SourceDocker
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
	Paused        bool          `json:"paused"` // every route answers 503; see POST /v1/pause
	Routes        []RouteStatus `json:"routes"`
}

// Event types sent on GET /v1/events.
const (
	EventRouteAdded    = "route.added"
	EventRouteUpdated  = "route.updated"
	EventRouteRemoved  = "route.removed"
	EventHealthChanged = "health.changed"
	EventPausedChanged = "paused.changed" // Route is empty; Paused is set
)

// Event is one server-sent event.
type Event struct {
	Type   string      `json:"type"`
	Route  RouteStatus `json:"route"`
	Paused *bool       `json:"paused,omitempty"` // for paused.changed
}

// Error is the body of every non-2xx response.
type Error struct {
	Error string `json:"error"`
}

// CAInfo is the body of GET /v1/ca.
type CAInfo struct {
	Present     bool     `json:"present"`
	Fingerprint string   `json:"fingerprint,omitempty"` // SHA-256, hex
	NotAfter    string   `json:"not_after,omitempty"`   // RFC 3339
	TLDs        []string `json:"tlds,omitempty"`        // what its name constraints allow
	Trusted     bool     `json:"trusted"`               // by the system trust store
	Error       string   `json:"error,omitempty"`       // why it isn't present or trusted
}

// ApplyRequest is the body of POST /v1/apply: the complete set of routes from
// one project file. An empty Routes removes all of that file's routes.
type ApplyRequest struct {
	File   string         `json:"file"` // absolute path of the switchboard.toml
	Routes []config.Route `json:"routes"`
}

// ApplyResult reports what POST /v1/apply changed.
type ApplyResult struct {
	Added     []config.Route `json:"added"`
	Updated   []config.Route `json:"updated"`
	Removed   []config.Route `json:"removed"`
	Unchanged []config.Route `json:"unchanged"`
	Conflicts []Conflict     `json:"conflicts"` // not applied
}

// Conflict is a route from the file whose name another source already has.
type Conflict struct {
	Name  string `json:"name"`
	Owner string `json:"owner"` // e.g. "a route added with 'sb add'"
}
