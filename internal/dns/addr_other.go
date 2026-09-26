//go:build !windows

package dns

// DefaultAddr is where the daemon binds DNS itself when the helper can't
// hand it sockets: before 'sb setup', or with a helper from an earlier
// version. macOS and Linux point split DNS at any port.
const DefaultAddr = "127.0.0.1:15353"

// ResolverAddr is where 'sb setup' points split DNS. Its port is privileged,
// so the root helper binds it and hands the sockets to the daemon: no other
// user can bind it first and answer .test lookups with addresses of their
// choosing.
const ResolverAddr = "127.0.0.1:535"
