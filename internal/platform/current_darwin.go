//go:build darwin

package platform

import "github.com/nanaaikinson/switchboard/internal/platform/darwin"

// New returns the macOS platform for o.
func New(o Options) Platform {
	return darwin.New(darwin.Options{UID: o.UID, User: o.User, Home: o.Home, SbPath: o.SbPath})
}
