//go:build !darwin && !linux && !windows

package platform

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"runtime"
)

var errUnsupported error = unsupportedError{}

type unsupportedError struct{}

func (unsupportedError) Error() string        { return "not supported on " + runtime.GOOS }
func (unsupportedError) Is(target error) bool { return target == errors.ErrUnsupported }

type unsupported struct{}

func (unsupported) OpenURL(string) error { return errUnsupported }
func (unsupported) AdminCommand([]string, string) (*exec.Cmd, error) {
	return nil, errUnsupported
}
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
func (unsupported) TrustPlan(string) []string                 { return nil }
func (unsupported) UntrustPlan(string) []string               { return nil }
func (unsupported) TrustCA(string) error                      { return errUnsupported }
func (unsupported) UntrustCA(string) error                    { return errUnsupported }
func (unsupported) TrustNSS(string) error                     { return errUnsupported }
func (unsupported) UntrustNSS(string) error                   { return errUnsupported }
func (unsupported) RestartDaemon() (bool, error)              { return false, nil }
func (unsupported) HelperRunning(context.Context) error       { return errUnsupported }
func (unsupported) SyncHosts(context.Context, []string) error { return errUnsupported }
func (unsupported) CheckResolver(string, int) error           { return errUnsupported }
func (unsupported) LookupHost(context.Context, string) ([]string, error) {
	return nil, errUnsupported
}
func (unsupported) PortOwner(context.Context, int) (string, error) { return "", errUnsupported }

// New returns a platform whose system features all report unsupported.
func New(Options) Platform { return unsupported{} }
