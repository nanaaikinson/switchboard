package windows

import (
	"context"
	"errors"
	"net"
)

// ErrUnsupported is returned by system setup, which is macOS-only so far.
var ErrUnsupported = errors.New("system setup is not implemented on windows yet")

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
