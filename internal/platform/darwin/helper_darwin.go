package darwin

import (
	"context"
	"errors"
	"net"

	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// ErrNoHelper means the privileged helper is not installed or not running.
var ErrNoHelper = posix.ErrNoHelper

// ServeHelper runs the root side of the helper protocol until ctx is done;
// see posix.Server. It binds p.o.HelperAddrs on the first request.
func (p *Platform) ServeHelper(ctx context.Context) error {
	gid, err := posix.UserGID(p.o.UID)
	if err != nil {
		return err
	}
	s := &posix.Server{Socket: p.o.HelperSocket, UID: p.o.UID, GID: gid, Addrs: p.o.HelperAddrs, DNSAddr: p.o.HelperDNSAddr, Build: p.o.Version}
	return s.Serve(ctx)
}

// HelperDNS asks the helper for the DNS server's sockets.
func (p *Platform) HelperDNS(ctx context.Context) (net.PacketConn, net.Listener, error) {
	return posix.DNSSockets(ctx, p.o.HelperSocket)
}

// HelperListeners asks the helper for the HTTP and HTTPS listening sockets.
func (p *Platform) HelperListeners(ctx context.Context) ([]net.Listener, error) {
	return posix.Listeners(ctx, p.o.HelperSocket)
}

// SyncHosts is unsupported: macOS always has split DNS through /etc/resolver.
func (p *Platform) SyncHosts(context.Context, []string) error { return errors.ErrUnsupported }
