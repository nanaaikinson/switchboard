package client

import (
	"context"
	"errors"
	"net"

	"github.com/nanaaikinson/switchboard/internal/api"
	pwin "github.com/nanaaikinson/switchboard/internal/platform/windows"
)

// errNoDaemon is how a missing pipe is reported.
var errNoDaemon = pwin.ErrPipeNotFound

// Dial connects to the control API's named pipe, and refuses a pipe served by
// another user.
func Dial(ctx context.Context, pipe string) (net.Conn, error) {
	c, err := pwin.DialPipe(ctx, pipe)
	return c, api.ExplainForeignPipe(err)
}

// untrusted reports whether err is Dial refusing the pipe.
func untrusted(err error) bool { return errors.Is(err, pwin.ErrForeignPipe) }
