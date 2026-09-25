//go:build !windows

package client

import (
	"context"
	"errors"
	"net"
)

// errNoDaemon is never returned on Unix: a failed dial is a *net.OpError.
var errNoDaemon = errors.New("no daemon")

// Dial connects to the control API's Unix socket.
func Dial(ctx context.Context, socketPath string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socketPath)
}
