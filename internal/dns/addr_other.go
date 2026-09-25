//go:build !windows

package dns

// DefaultAddr is the default listen address for UDP and TCP. macOS and Linux
// point split DNS at any port, so an unprivileged one is used.
const DefaultAddr = "127.0.0.1:15353"
