package linux

import (
	"context"
	"errors"
	"net"
)

// ErrUnsupported is returned by system setup, which is macOS-only so far. It
// matches errors.ErrUnsupported.
var ErrUnsupported error = unsupportedError{}

type unsupportedError struct{}

func (unsupportedError) Error() string        { return "system setup is not implemented on linux yet" }
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
