//go:build windows

package platform

import "github.com/nanaaikinson/switchboard/internal/platform/windows"

// New returns the windows platform. System setup is not implemented yet.
func New(Options) Platform { return windows.Platform{} }
