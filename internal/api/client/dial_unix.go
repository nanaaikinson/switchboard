//go:build !windows

package client

import (
	"context"
	"errors"
	"net"

	"github.com/nanaaikinson/switchboard/internal/api"
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
	return d.DialContext(ctx, "unix", socketPath)
}

// untrusted reports whether err is Dial refusing the socket.
func untrusted(err error) bool { return errors.Is(err, api.ErrUntrustedSocket) }
