//go:build darwin

package platform

import "github.com/nanaaikinson/switchboard/internal/platform/darwin"

// Current returns the platform for this build.
func Current() Platform { return darwin.Platform{} }
