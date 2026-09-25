package windows

import (
	"context"
	"errors"
	"net"
	"os/exec"
)

// ErrUnsupported is returned by system setup, which is macOS and Linux only so far. It
// matches errors.ErrUnsupported.
var ErrUnsupported error = unsupportedError{}

type unsupportedError struct{}

func (unsupportedError) Error() string        { return "system setup is not implemented on windows yet" }
func (unsupportedError) Is(target error) bool { return target == errors.ErrUnsupported }

// Validate reports that setup is unsupported.
func (Platform) Validate() error { return ErrUnsupported }

// InstallPlan is empty: nothing would be changed.
func (Platform) InstallPlan(string, int) []string { return nil }

// UninstallPlan is empty: nothing would be changed.
func (Platform) UninstallPlan(string) []string { return nil }

// InstallResolver is unsupported.
func (Platform) InstallResolver(string, int) error { return ErrUnsupported }

// RemoveResolver is unsupported.
func (Platform) RemoveResolver(string) error { return ErrUnsupported }

// InstallService is unsupported.
func (Platform) InstallService() error { return ErrUnsupported }

// RemoveService is unsupported.
func (Platform) RemoveService() error { return ErrUnsupported }

// ServeHelper is unsupported.
func (Platform) ServeHelper(context.Context) error { return ErrUnsupported }

// HelperListeners is unsupported; the daemon binds ports itself.
func (Platform) HelperListeners(context.Context) ([]net.Listener, error) { return nil, ErrUnsupported }

// HelperRunning is unsupported.
func (Platform) HelperRunning(context.Context) error { return ErrUnsupported }

// CheckResolver is unsupported.
func (Platform) CheckResolver(string, int) error { return ErrUnsupported }

// LookupHost is unsupported.
func (Platform) LookupHost(context.Context, string) ([]string, error) { return nil, ErrUnsupported }

// PortOwner is unsupported.
func (Platform) PortOwner(context.Context, int) (string, error) { return "", ErrUnsupported }

// TrustPlan is empty: nothing would be changed.
func (Platform) TrustPlan(string) []string { return nil }

// UntrustPlan is empty: nothing would be changed.
func (Platform) UntrustPlan(string) []string { return nil }

// TrustCA is unsupported.
func (Platform) TrustCA(string) error { return ErrUnsupported }

// UntrustCA is unsupported.
func (Platform) UntrustCA(string) error { return ErrUnsupported }

// TrustNSS is unsupported.
func (Platform) TrustNSS(string) error { return ErrUnsupported }

// UntrustNSS is unsupported.
func (Platform) UntrustNSS(string) error { return ErrUnsupported }

// SyncHosts is unsupported.
func (Platform) SyncHosts(context.Context, []string) error { return ErrUnsupported }

// AdminCommand is unsupported.
func (Platform) AdminCommand([]string, string) (*exec.Cmd, error) { return nil, ErrUnsupported }

// RestartDaemon does nothing: there is no daemon service on Windows yet.
func (Platform) RestartDaemon() (bool, error) { return false, nil }
