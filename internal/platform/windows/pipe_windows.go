package windows

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// ErrPipeNotFound means nothing serves the pipe: the daemon isn't running.
var ErrPipeNotFound = errors.New("named pipe not found")

// CurrentSID is the SID of the user running this process.
func CurrentSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("look up the current user: %w", err)
	}
	return u.User.Sid.String(), nil
}

// ListenPipe serves the named pipe for the current user only. It fails if the
// pipe exists already, whoever made it: winio creates the first instance
// with FILE_FLAG_FIRST_PIPE_INSTANCE, so a squatter can't sit in front.
func ListenPipe(name string) (net.Listener, error) {
	sid, err := CurrentSID()
	if err != nil {
		return nil, err
	}
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: PipeSDDL(sid)})
	// FILE_FLAG_FIRST_PIPE_INSTANCE reports an existing pipe as access denied.
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, fmt.Errorf("a daemon is already running on %s (or another program holds that pipe); stop it first", name)
	}
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w; is another Switchboard daemon running?", name, err)
	}
	return ln, nil
}

// DialPipe connects to the named pipe and checks that its server runs as the
// current user, so a pipe another account created under the same name is
// refused rather than trusted.
func DialPipe(ctx context.Context, name string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil, fmt.Errorf("%w: %s", ErrPipeNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	if err := checkServerUser(c); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func checkServerUser(c net.Conn) error {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("named pipe connection has no handle")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return fmt.Errorf("find the pipe's server process: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("open the pipe's server process %d: %w", pid, err)
	}
	defer windows.CloseHandle(proc) //nolint:errcheck // closing a query handle
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return fmt.Errorf("the pipe's server process %d runs as another user; refusing it: %w", pid, err)
	}
	defer tok.Close()
	theirs, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	mine, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if !windows.EqualSid(theirs.User.Sid, mine.User.Sid) {
		return fmt.Errorf("the pipe is served by another user (%s), not you; refusing it", theirs.User.Sid)
	}
	return nil
}
