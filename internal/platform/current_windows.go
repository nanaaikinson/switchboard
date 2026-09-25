//go:build windows

package platform

import "github.com/nanaaikinson/switchboard/internal/platform/windows"

// Current returns the platform for this build.
func Current() Platform { return windows.Platform{} }
