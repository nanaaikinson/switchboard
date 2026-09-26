package client

import (
	"context"
	"net"

	pwin "github.com/nanaaikinson/switchboard/internal/platform/windows"
)

// errNoDaemon is how a missing pipe is reported.
var errNoDaemon = pwin.ErrPipeNotFound

// Dial connects to the control API's named pipe, and refuses a pipe served by
// another user.
func Dial(ctx context.Context, pipe string) (net.Conn, error) { return pwin.DialPipe(ctx, pipe) }

// untrusted reports whether err is Dial refusing the pipe. Its errors are
// passed on with their request context.
func untrusted(error) bool { return false }
