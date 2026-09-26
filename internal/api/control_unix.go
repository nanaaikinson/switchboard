//go:build !windows

package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// maxSocketPath stays under the smallest sun_path limit (104 bytes on macOS).
const maxSocketPath = 100

// ErrUntrustedSocket means the control socket, or its directory, belongs to
// another user or can be written by one, so whoever answers on it may not be
// this user's daemon.
var ErrUntrustedSocket = errors.New("untrusted control socket")

var geteuid = os.Geteuid // replaced in tests, which can't create other users' files

// DefaultSocketPath returns the control socket path in the config dir.
func DefaultSocketPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SocketName), nil
}

// ListenControl listens on the control socket at path: a Unix socket, mode
// 0600. On Windows it is a named pipe instead.
func ListenControl(path string) (net.Listener, error) { return ListenUnix(path) }

// ListenUnix creates the control socket at path with mode 0600. A stale socket
// is replaced; a live one means another daemon is running. The directory must
// be the user's own and writable by nobody else, since whoever can write to
// it could replace the socket and pose as the daemon.
func ListenUnix(path string) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("api: socket path %s is too long (%d > %d bytes); set %s to a shorter directory",
			path, len(path), maxSocketPath, config.EnvConfigDir)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("api: create socket dir: %w", err)
	}
	if err := checkDir(dir); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("api: a daemon is already running on %s; stop it first", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("api: remove stale socket %s: %w", path, err)
		}
	}

	// Bind in a new private directory, chmod, then move the socket into
	// place, so it never exists at path with the umask's permissions.
	// Setting the umask around Listen would do too, but it is process-wide:
	// a file another goroutine created meanwhile would get the wrong mode.
	// The short fixed name keeps the temporary path shorter than path.
	tmpDir := filepath.Join(dir, ".sock")
	if err := os.RemoveAll(tmpDir); err != nil { // left by a crash
		return nil, fmt.Errorf("api: remove %s: %w", tmpDir, err)
	}
	if err := os.Mkdir(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("api: create socket dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmp := filepath.Join(tmpDir, "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("api: listen on %s: %w", path, err)
	}
	ln.SetUnlinkOnClose(false) // it only knows tmp; socketListener removes path
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("api: chmod socket: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("api: listen on %s: %w", path, err)
	}
	return &socketListener{UnixListener: ln, path: path}, nil
}

// socketListener removes the socket at path when closed.
type socketListener struct {
	*net.UnixListener
	path string
	once sync.Once
}

func (l *socketListener) Close() error {
	err := l.UnixListener.Close()
	l.once.Do(func() { _ = os.Remove(l.path) })
	return err
}

// CheckSocket returns an ErrUntrustedSocket error unless the socket at path
// and its directory are the user's own and nobody else can write to the
// directory. The CLI calls it before trusting whoever answers on the socket.
// A missing socket is fine: dialing it then reports that no daemon runs.
func CheckSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("api: check socket: %w", err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%w: %s is not a socket; remove it and start the daemon again with 'sb daemon'", ErrUntrustedSocket, path)
	}
	if uid := owner(fi); uid != geteuid() {
		return fmt.Errorf("%w: %s belongs to user %d, not you (%d), so it may not be your daemon; remove it, or set %s to a directory of your own",
			ErrUntrustedSocket, path, uid, geteuid(), config.EnvConfigDir)
	}
	return checkDir(filepath.Dir(path))
}

// checkDir requires dir, and the target if it is a symlink, to belong to the
// user and to be writable by nobody else. Others may still read it: the
// socket itself is mode 0600.
func checkDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("api: check socket dir: %w", err)
	}
	euid := geteuid()
	if fi.Mode().Type() == fs.ModeSymlink {
		if uid := owner(fi); uid != euid {
			return fmt.Errorf("%w: %s is a symlink owned by user %d, not you (%d); set %s to a directory of your own",
				ErrUntrustedSocket, dir, uid, euid, config.EnvConfigDir)
		}
		if fi, err = os.Stat(dir); err != nil {
			return fmt.Errorf("api: check socket dir: %w", err)
		}
	}
	switch {
	case !fi.IsDir():
		return fmt.Errorf("%w: %s is not a directory; set %s to a directory of your own", ErrUntrustedSocket, dir, config.EnvConfigDir)
	case owner(fi) != euid:
		return fmt.Errorf("%w: %s belongs to user %d, not you (%d), who could replace the control socket; set %s to a directory of your own",
			ErrUntrustedSocket, dir, owner(fi), euid, config.EnvConfigDir)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%w: other users can write to %s (mode %o) and could replace the control socket; run: chmod 700 %s",
			ErrUntrustedSocket, dir, fi.Mode().Perm(), dir)
	}
	return nil
}

func owner(fi fs.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}
