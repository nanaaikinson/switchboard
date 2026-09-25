//go:build !windows

package api

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenUnix(t *testing.T) {
	path := filepath.Join(shortDir(t), SocketName)
	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 600", got)
	}
	if _, err := ListenUnix(path); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second ListenUnix err = %v, want already running", err)
	}

	// Simulate a crash: the socket file stays but nothing listens.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	ln2, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("ListenUnix over stale socket: %v", err)
	}
	_ = ln2.Close()

	long := filepath.Join(shortDir(t), strings.Repeat("x", 120), SocketName)
	if _, err := ListenUnix(long); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("long path err = %v", err)
	}
}

// shortDir returns a temp dir short enough for a Unix socket path.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
