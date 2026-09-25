package docker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client is the part of the Docker Engine API Switchboard uses. Podman,
// OrbStack, Colima and Rancher Desktop serve the same API.
type Client interface {
	// Containers lists running containers.
	Containers(ctx context.Context) ([]Container, error)
	// Events streams container lifecycle events until ctx is done or the
	// connection fails; the error channel then receives one error.
	Events(ctx context.Context) (<-chan Event, <-chan error)
}

// Event is one container event.
type Event struct {
	Action string `json:"Action"`
	Actor  struct {
		ID string `json:"ID"`
	} `json:"Actor"`
}

// eventFilter limits the stream to changes that can add or remove a route.
const eventFilter = `{"type":["container"],"event":["start","die","destroy","rename","pause","unpause","update"]}`

// Endpoint is a Docker API address.
type Endpoint struct {
	Network string      // "unix" or "tcp"
	Addr    string      // socket path or host:port
	TLS     *tls.Config // for tcp with DOCKER_TLS_VERIFY
}

func (e Endpoint) String() string {
	if e.Network == "unix" {
		return "unix://" + e.Addr
	}
	return "tcp://" + e.Addr
}

// Env is the environment discovery reads; zero fields use the real process.
type Env struct {
	Getenv func(string) string
	Home   string
	Probe  []string // sockets to try; nil is Sockets()
}

func (e Env) get(k string) string {
	if e.Getenv == nil {
		return os.Getenv(k)
	}
	return e.Getenv(k)
}

func (e Env) home() string {
	if e.Home != "" {
		return e.Home
	}
	h, _ := os.UserHomeDir()
	return h
}

// Sockets are the default Docker-compatible sockets probed, in order, when
// DOCKER_HOST is unset.
func (e Env) Sockets() []string {
	home := e.home()
	socks := []string{
		"/var/run/docker.sock",
		filepath.Join(home, ".docker/run/docker.sock"),   // Docker Desktop (macOS)
		filepath.Join(home, ".orbstack/run/docker.sock"), // OrbStack
		filepath.Join(home, ".colima/default/docker.sock"),
		filepath.Join(home, ".colima/docker.sock"), // Colima before 0.4
		filepath.Join(home, ".rd/docker.sock"),     // Rancher Desktop
	}
	if run := e.get("XDG_RUNTIME_DIR"); run != "" {
		socks = append(socks, filepath.Join(run, "podman/podman.sock")) // rootless Podman
	}
	return append(socks,
		"/run/podman/podman.sock",
		filepath.Join(home, ".local/share/containers/podman/machine/podman.sock"), // Podman machine (macOS)
	)
}

// Find returns the Docker endpoint: DOCKER_HOST if set, else the first
// default socket that answers a ping.
func Find(ctx context.Context, env Env) (Endpoint, error) {
	if host := env.get("DOCKER_HOST"); host != "" {
		return parseHost(host, env)
	}
	probe := env.Probe
	if probe == nil {
		probe = env.Sockets()
	}
	var tried []string
	for _, s := range probe {
		if fi, err := os.Stat(s); err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		ep := Endpoint{Network: "unix", Addr: s}
		if err := NewClient(ep).Ping(ctx); err != nil {
			tried = append(tried, fmt.Sprintf("%s (%v)", s, err))
			continue
		}
		return ep, nil
	}
	if len(tried) > 0 {
		return Endpoint{}, fmt.Errorf("no Docker socket answered: %s", strings.Join(tried, "; "))
	}
	return Endpoint{}, errors.New("no container engine running: DOCKER_HOST is unset and no Docker, OrbStack, Colima, Rancher Desktop or Podman socket was found")
}

func parseHost(host string, env Env) (Endpoint, error) {
	u, err := url.Parse(host)
	if err != nil {
		return Endpoint{}, fmt.Errorf("DOCKER_HOST=%q: %w", host, err)
	}
	switch u.Scheme {
	case "unix":
		return Endpoint{Network: "unix", Addr: u.Path}, nil
	case "tcp":
		ep := Endpoint{Network: "tcp", Addr: u.Host}
		if env.get("DOCKER_TLS_VERIFY") != "" {
			dir := env.get("DOCKER_CERT_PATH")
			if dir == "" {
				dir = filepath.Join(env.home(), ".docker")
			}
			if ep.TLS, err = tlsConfig(dir, u.Hostname()); err != nil {
				return Endpoint{}, fmt.Errorf("DOCKER_HOST=%q with DOCKER_TLS_VERIFY: %w", host, err)
			}
		}
		return ep, nil
	default:
		return Endpoint{}, fmt.Errorf("DOCKER_HOST=%q: only unix:// and tcp:// are supported; Switchboard routes to ports on this machine, so point it at a local socket", host)
	}
}

func tlsConfig(dir, server string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem")) //nolint:gosec // G304: the user's Docker cert dir
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no certificates in %s", filepath.Join(dir, "ca.pem"))
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		return nil, err
	}
	return &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: server, MinVersion: tls.VersionTLS12}, nil
}

// HTTPClient talks to the Docker Engine API. The paths are unversioned, so
// the daemon serves its own API version; the fields used are stable across
// versions and in Podman's compatible API.
type HTTPClient struct {
	ep     Endpoint
	base   string
	calls  *http.Client // with a timeout
	stream *http.Client // for /events, which never ends
}

var _ Client = (*HTTPClient)(nil)

// NewClient returns a client for ep. It doesn't connect until used.
func NewClient(ep Endpoint) *HTTPClient {
	var d net.Dialer
	tr := &http.Transport{
		Proxy:           nil, // the Docker API is local; never use HTTP_PROXY
		TLSClientConfig: ep.TLS,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, ep.Network, ep.Addr)
		},
	}
	base := "http://docker"
	if ep.TLS != nil {
		base = "https://" + ep.Addr
	}
	return &HTTPClient{ep: ep, base: base, calls: &http.Client{Transport: tr, Timeout: 10 * time.Second}, stream: &http.Client{Transport: tr}}
}

// Ping checks that the endpoint serves the Docker API.
func (c *HTTPClient) Ping(ctx context.Context) error {
	resp, err := c.get(ctx, c.calls, "/_ping")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Containers implements Client.
func (c *HTTPClient) Containers(ctx context.Context) ([]Container, error) {
	resp, err := c.get(ctx, c.calls, "/containers/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []Container
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("docker: decode containers: %w", err)
	}
	return out, nil
}

// Events implements Client.
func (c *HTTPClient) Events(ctx context.Context) (<-chan Event, <-chan error) {
	events, errc := make(chan Event), make(chan error, 1)
	go func() {
		defer close(events)
		resp, err := c.get(ctx, c.stream, "/events?filters="+url.QueryEscape(eventFilter))
		if err != nil {
			errc <- err
			return
		}
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			var ev Event
			if err := dec.Decode(&ev); err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				errc <- fmt.Errorf("docker: event stream: %w", err)
				return
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			}
		}
	}()
	return events, errc
}

func (c *HTTPClient) get(ctx context.Context, hc *http.Client, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w", c.ep, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("docker %s: GET %s: %s: %s", c.ep, strings.SplitN(path, "?", 2)[0], resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}
