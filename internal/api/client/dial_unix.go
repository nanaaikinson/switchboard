//go:build !windows

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/config"
)

// errNoDaemon is never returned on Unix: a failed dial is a *net.OpError.
var errNoDaemon = errors.New("no daemon")

// Dial connects to the control API's Unix socket, and refuses a socket that
// another user owns or could have replaced: whoever answers on it could pose
// as the daemon, and the CLI hands its answers, such as dashboard sign-in
// links, to the user.
func Dial(ctx context.Context, socketPath string) (net.Conn, error) {
	if err := api.CheckSocket(socketPath); err != nil {
		return nil, err
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, err
	}
	// The path checks above could be raced by swapping the socket; the uid
	// of the process that accepted can't be.
	uid, err := peerUID(c.(*net.UnixConn))
	if err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = c.Close()
		return nil, fmt.Errorf("api: check who serves %s: %w", socketPath, err)
	}
	if err == nil && uid != os.Geteuid() {
		_ = c.Close()
		return nil, fmt.Errorf("%w: %s is served by user %d, not you (%d), so it is not your daemon; set %s to a directory of your own",
			api.ErrUntrustedSocket, socketPath, uid, os.Geteuid(), config.EnvConfigDir)
	}
	return c, nil
}

// untrusted reports whether err is Dial refusing the socket.
func untrusted(err error) bool { return errors.Is(err, api.ErrUntrustedSocket) }
