// Package platform exposes OS-specific behaviour behind an interface. The
// implementation for the build target lives in internal/platform/<os>.
package platform

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

// Platform is the OS-specific functionality Switchboard needs.
type Platform interface {
	// OpenURL opens an http(s) URL in the user's default browser.
	OpenURL(rawURL string) error

	// Validate checks Options before any privileged change.
	Validate() error
	// InstallPlan and UninstallPlan describe, in order, what setup and
	// uninstall change, so sb can show them before asking for sudo.
	InstallPlan(tld string, dnsPort int) []string
	UninstallPlan(tld string) []string

	// Privileged; run by 'sb helper' as root.
	InstallResolver(tld string, port int) error
	RemoveResolver(tld string) error
	InstallService() error
	RemoveService() error

	// ServeHelper is the root side of the helper protocol: it hands the HTTP
	// listening sockets to the user's daemon. HelperListeners is the daemon side.
	ServeHelper(ctx context.Context) error
	HelperListeners(ctx context.Context) ([]net.Listener, error)

	// Diagnostics for 'sb doctor', run as the user. Failures may implement
	// interface{ Fix() string } with a one-line fix. Unsupported checks
	// return an error matching errors.ErrUnsupported.
	HelperRunning(ctx context.Context) error
	CheckResolver(tld string, port int) error
	// LookupHost resolves host through the system resolver, as apps do.
	LookupHost(ctx context.Context, host string) ([]string, error)
	// PortOwner names the process listening on a TCP port, or "" if none is
	// visible to this user.
	PortOwner(ctx context.Context, port int) (string, error)
}

// Options identifies the user Switchboard is installed for.
type Options struct {
	UID    int
	Home   string
	SbPath string // absolute path of the sb binary the user runs
}

// Current returns the platform for the current process's user and binary.
func Current() Platform {
	o := Options{UID: os.Getuid()}
	o.Home, _ = os.UserHomeDir()
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		o.SbPath = exe
	}
	return New(o)
}

// OpenURL validates rawURL and opens it with the current platform.
func OpenURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("open %q: only http(s) URLs can be opened", rawURL)
	}
	if err := Current().OpenURL(u.String()); err != nil {
		return fmt.Errorf("open %s in browser: %w; open it manually", u, err)
	}
	return nil
}
