package api

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/nanaaikinson/switchboard/internal/config"
	pwin "github.com/nanaaikinson/switchboard/internal/platform/windows"
)

// DefaultSocketPath returns the control API's named pipe for the current
// user and config dir (see pwin.PipeName). Its random part comes from the
// config dir, which the daemon and the CLI share.
func DefaultSocketPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	sid, err := pwin.CurrentSID()
	if err != nil {
		return "", err
	}
	id, err := pwin.PipeID(dir)
	if err != nil {
		return "", fmt.Errorf("control pipe name: %w", err)
	}
	return pwin.PipeName(sid, dir, id), nil
}

// ListenControl serves the named pipe, which only the current user can open.
func ListenControl(name string) (net.Listener, error) {
	ln, err := pwin.ListenPipe(name)
	return ln, ExplainForeignPipe(err)
}

// ExplainForeignPipe adds what to do to an error that wraps
// pwin.ErrForeignPipe: another account took the pipe's name first, which a
// new random name gets around. Other errors are returned as they are.
func ExplainForeignPipe(err error) error {
	if !errors.Is(err, pwin.ErrForeignPipe) {
		return err
	}
	dir, derr := config.Dir()
	if derr != nil {
		return err
	}
	return fmt.Errorf("%w. Delete %s to pick a new pipe name, then start the daemon again (re-run 'sb setup', or run 'sb daemon')",
		err, filepath.Join(dir, pwin.PipeIDFile))
}
