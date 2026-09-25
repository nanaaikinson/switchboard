//go:build darwin || linux

// Package posix holds the Unix pieces shared by the darwin and linux
// platforms: the privileged helper's socket protocol, root-safe writes into a
// user's home, CA file validation and diagnostic errors that carry a fix.
package posix
