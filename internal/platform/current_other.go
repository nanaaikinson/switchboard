//go:build !darwin && !linux && !windows

package platform

import (
	"context"
	"errors"
	"net"
	"runtime"
)

var errUnsupported = errors.New("not supported on " + runtime.GOOS)

type unsupported struct{}

func (unsupported) OpenURL(string) error              { return errUnsupported }
func (unsupported) Validate() error                   { return errUnsupported }
func (unsupported) InstallPlan(string, int) []string  { return nil }
func (unsupported) UninstallPlan(string) []string     { return nil }
func (unsupported) InstallResolver(string, int) error { return errUnsupported }
func (unsupported) RemoveResolver(string) error       { return errUnsupported }
func (unsupported) InstallService() error             { return errUnsupported }
func (unsupported) RemoveService() error              { return errUnsupported }
func (unsupported) ServeHelper(context.Context) error { return errUnsupported }
func (unsupported) HelperListeners(context.Context) ([]net.Listener, error) {
	return nil, errUnsupported
}

// New returns a platform whose system features all report unsupported.
func New(Options) Platform { return unsupported{} }
