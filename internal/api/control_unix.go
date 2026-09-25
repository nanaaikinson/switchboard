//go:build !windows

package api

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// maxSocketPath stays under the smallest sun_path limit (104 bytes on macOS).
const maxSocketPath = 100

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
// is replaced; a live one means another daemon is running.
func ListenUnix(path string) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("api: socket path %s is too long (%d > %d bytes); set %s to a shorter directory",
			path, len(path), maxSocketPath, config.EnvConfigDir)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("api: create socket dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("api: a daemon is already running on %s; stop it first", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("api: remove stale socket %s: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("api: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("api: chmod socket: %w", err)
	}
	return ln, nil
}
