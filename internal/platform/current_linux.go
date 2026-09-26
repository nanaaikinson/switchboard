//go:build linux

package platform

import "github.com/nanaaikinson/switchboard/internal/platform/linux"

// New returns the Linux (systemd) platform for o.
func New(o Options) Platform {
	return linux.New(linux.Options{UID: o.UID, User: o.User, Home: o.Home, SbPath: o.SbPath})
}
