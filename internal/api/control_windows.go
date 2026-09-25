package api

import (
	"net"

	"github.com/nanaaikinson/switchboard/internal/config"
	pwin "github.com/nanaaikinson/switchboard/internal/platform/windows"
)

// DefaultSocketPath returns the control API's named pipe for the current
// user and config dir (see pwin.PipeName).
func DefaultSocketPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	sid, err := pwin.CurrentSID()
	if err != nil {
		return "", err
	}
	return pwin.PipeName(sid, dir), nil
}

// ListenControl serves the named pipe, which only the current user can open.
func ListenControl(name string) (net.Listener, error) { return pwin.ListenPipe(name) }
