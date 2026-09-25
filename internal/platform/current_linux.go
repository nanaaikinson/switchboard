//go:build linux

package platform

import "github.com/nanaaikinson/switchboard/internal/platform/linux"

// New returns the linux platform. System setup is not implemented yet.
func New(Options) Platform { return linux.Platform{} }
