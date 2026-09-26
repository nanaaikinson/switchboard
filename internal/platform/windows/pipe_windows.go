package windows

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

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
// with FILE_FLAG_FIRST_PIPE_INSTANCE, so a squatter can't sit in front. When
// another account holds the name the error wraps ErrForeignPipe and names it.
func ListenPipe(name string) (net.Listener, error) {
	sid, err := CurrentSID()
	if err != nil {
		return nil, err
	}
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: PipeSDDL(sid)})
	// FILE_FLAG_FIRST_PIPE_INSTANCE reports an existing pipe as access denied.
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, whoHolds(name)
	}
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w; is another Switchboard daemon running?", name, err)
	}
	return newClosableListener(ln), nil
}

// whoHolds explains why an existing pipe can't be served: our own daemon
// runs, or another account (or a program we can't identify) made it first.
func whoHolds(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := DialPipe(ctx, name)
	switch {
	case err == nil:
		_ = c.Close()
		return fmt.Errorf("a daemon is already running on %s; stop it first", name)
	case errors.Is(err, ErrForeignPipe):
		return fmt.Errorf("can't serve %s: %w", name, err)
	}
	return fmt.Errorf("can't serve %s: a daemon is already running, or another program holds that pipe (%w); stop it first", name, err)
}

// DialPipe connects to the named pipe and checks that its server runs as the
// current user, so a pipe another account created under the same name is
// refused rather than trusted.
func DialPipe(ctx context.Context, name string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil, fmt.Errorf("%w: %s", ErrPipeNotFound, name)
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// Our daemon's DACL always lets us in, unless it runs elevated
		// and we don't.
		return nil, fmt.Errorf("%s exists but refuses you: another account may have created it, or the daemon runs as administrator and this command doesn't: %w", name, err)
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
	h := windows.Handle(f.Fd())
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return fmt.Errorf("find the pipe's server process: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("%w: its server process %d can't be inspected, so it isn't yours (owner: %s); refusing it: %w",
			ErrForeignPipe, pid, pipeOwner(h), err)
	}
	defer windows.CloseHandle(proc) //nolint:errcheck // closing a query handle
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return fmt.Errorf("%w: its server process %d runs as another user (owner: %s); refusing it: %w",
			ErrForeignPipe, pid, pipeOwner(h), err)
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
		return fmt.Errorf("%w: it is served by %s, not you; refusing it", ErrForeignPipe, account(theirs.User.Sid))
	}
	return nil
}

// pipeOwner names the account that owns the pipe h is connected to.
func pipeOwner(h windows.Handle) string {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "unknown"
	}
	sid, _, err := sd.Owner()
	if err != nil || sid == nil {
		return "unknown"
	}
	return account(sid)
}

// account is DOMAIN\user for sid, or the SID itself if it can't be looked up.
func account(sid *windows.SID) string {
	if user, domain, _, err := sid.LookupAccount(""); err == nil {
		return domain + `\` + user + " (" + sid.String() + ")"
	}
	return sid.String()
}
