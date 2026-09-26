// Package client talks to the Switchboard daemon's control API over its socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/config"
)

// ErrDaemonNotRunning means nothing is listening on the control socket.
var ErrDaemonNotRunning = errors.New("the Switchboard daemon is not running")

// IsUntrusted reports whether err is the client refusing a control socket or
// pipe that another user owns or could have replaced; the error says what to
// do.
func IsUntrusted(err error) bool { return untrusted(err) }

// Client is a control API client.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a client for the daemon listening on socketPath: a Unix
// socket, or a named pipe on Windows (see api.DefaultSocketPath).
func New(socketPath string) *Client {
	return &Client{
		socket: socketPath,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return Dial(ctx, socketPath)
				},
			},
		},
	}
}

// Routes lists routes with their health.
func (c *Client) Routes(ctx context.Context) ([]api.RouteStatus, error) {
	var out []api.RouteStatus
	_, err := c.do(ctx, http.MethodGet, "/v1/routes", nil, &out)
	return out, err
}

// Put adds or replaces a route. It returns the route as stored (with its
// qualified name) and whether it is new.
func (c *Client) Put(ctx context.Context, r config.Route) (api.RouteStatus, bool, error) {
	var out api.RouteStatus
	code, err := c.do(ctx, http.MethodPost, "/v1/routes", r, &out)
	return out, code == http.StatusCreated, err
}

// Delete removes a route and returns it.
func (c *Client) Delete(ctx context.Context, name string) (config.Route, error) {
	var out config.Route
	_, err := c.do(ctx, http.MethodDelete, "/v1/routes/"+url.PathEscape(name), nil, &out)
	return out, err
}

// Apply makes routes the complete set of routes from the project file at file.
func (c *Client) Apply(ctx context.Context, file string, routes []config.Route) (api.ApplyResult, error) {
	var out api.ApplyResult
	_, err := c.do(ctx, http.MethodPost, "/v1/apply", api.ApplyRequest{File: file, Routes: routes}, &out)
	return out, err
}

// DashboardLogin returns a one-time token for the dashboard's /login page.
// Only the control socket hands these out.
func (c *Client) DashboardLogin(ctx context.Context) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	_, err := c.do(ctx, http.MethodPost, "/v1/dashboard/login", struct{}{}, &out)
	return out.Token, err
}

// SetPaused turns every route off (true) or back on.
func (c *Client) SetPaused(ctx context.Context, paused bool) error {
	var out struct {
		Paused bool `json:"paused"`
	}
	_, err := c.do(ctx, http.MethodPost, "/v1/pause", map[string]bool{"paused": paused}, &out)
	return err
}

// TLDs lists the served TLDs, default first.
func (c *Client) TLDs(ctx context.Context) ([]api.TLD, error) {
	var out []api.TLD
	_, err := c.do(ctx, http.MethodGet, "/v1/tlds", nil, &out)
	return out, err
}

// AddTLD adds an opt-in TLD; only .local with mdns is supported. It reports
// whether the TLD is new.
func (c *Client) AddTLD(ctx context.Context, name string, mdns bool) (bool, error) {
	var out []api.TLD
	code, err := c.do(ctx, http.MethodPut, "/v1/tlds/"+url.PathEscape(name), map[string]bool{"mdns": mdns}, &out)
	return code == http.StatusCreated, err
}

// RemoveTLD removes an opt-in TLD.
func (c *Client) RemoveTLD(ctx context.Context, name string) error {
	var out []api.TLD
	_, err := c.do(ctx, http.MethodDelete, "/v1/tlds/"+url.PathEscape(name), nil, &out)
	return err
}

// CA reports the local CA and whether the system trusts it.
func (c *Client) CA(ctx context.Context) (api.CAInfo, error) {
	var out api.CAInfo
	_, err := c.do(ctx, http.MethodGet, "/v1/ca", nil, &out)
	return out, err
}

// Status returns daemon status.
func (c *Client) Status(ctx context.Context) (api.Status, error) {
	var out api.Status
	_, err := c.do(ctx, http.MethodGet, "/v1/status", nil, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://sb"+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.Is(err, errNoDaemon) || errors.As(err, &opErr) && opErr.Op == "dial" {
			return 0, fmt.Errorf("%w (no daemon on %s); start it with 'sb daemon'", ErrDaemonNotRunning, c.socket)
		}
		var uerr *url.Error
		if untrusted(err) && errors.As(err, &uerr) {
			return 0, uerr.Err // already says what's wrong and what to do
		}
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e api.Error
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return resp.StatusCode, &StatusError{Code: resp.StatusCode, Message: e.Error}
		}
		return resp.StatusCode, &StatusError{Code: resp.StatusCode, Message: resp.Status}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode %s response: %w", path, err)
	}
	return resp.StatusCode, nil
}

// StatusError is a non-2xx response from the daemon.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string { return e.Message }
