//go:build windows

package platform

import "github.com/nanaaikinson/switchboard/internal/platform/windows"

// New returns the Windows platform for o.
func New(o Options) Platform {
	return windows.New(windows.Options{User: o.User, SbPath: o.SbPath})
}
