//go:build linux

package platform

import "github.com/nanaaikinson/switchboard/internal/platform/linux"

// Current returns the platform for this build.
func Current() Platform { return linux.Platform{} }
