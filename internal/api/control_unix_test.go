//go:build !windows

package api

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenUnix(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, SocketName)
	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 600", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("socket dir has %d entries, want only the socket", len(entries))
	}
	if err := CheckSocket(path); err != nil {
		t.Errorf("CheckSocket on our own socket: %v", err)
	}
	if _, err := ListenUnix(path); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second ListenUnix err = %v, want already running", err)
	}

	// Simulate a crash: the socket file stays but nothing listens.
	_ = ln.(*socketListener).UnixListener.Close()
	ln2, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("ListenUnix over stale socket: %v", err)
	}
	if c, err := net.Dial("unix", path); err != nil {
		t.Errorf("dial the moved socket: %v", err)
	} else {
		_ = c.Close()
	}
	_ = ln2.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("socket still there after Close: %v", err)
	}

	long := filepath.Join(shortDir(t), strings.Repeat("x", 120), SocketName)
	if _, err := ListenUnix(long); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("long path err = %v", err)
	}
}

// Whoever can write to the socket's directory can replace the socket and pose
// as the daemon, so neither the daemon nor the CLI trusts such a directory.
func TestSocketDirMustBeOurs(t *testing.T) {
	symlinked := func(t *testing.T) string {
		link := filepath.Join(shortDir(t), "l")
		if err := os.Symlink(shortDir(t), link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	tests := []struct {
		name    string
		dir     func(t *testing.T) string
		mode    os.FileMode // the dir's mode once the socket is in it; 0 keeps 0700
		foreign bool        // the files belong to someone else
		want    string      // in the error; "" for none
	}{
		{"private dir", shortDir, 0, false, ""},
		{"readable by others is fine", shortDir, 0o755, false, ""},
		{"group-writable", shortDir, 0o770, false, "chmod 700"},
		{"world-writable, sticky", shortDir, 0o777 | os.ModeSticky, false, "chmod 700"},
		{"another user's dir and socket", shortDir, 0, true, "belongs to user"},
		{"symlink to our own dir", symlinked, 0, false, ""},
		{"symlink to a writable dir", symlinked, 0o777, false, "chmod 700"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.dir(t)
			path := filepath.Join(dir, SocketName)
			ln, err := ListenUnix(path)
			if err != nil {
				t.Fatal(err)
			}
			if tt.mode != 0 {
				if err := os.Chmod(dir, tt.mode); err != nil { // follows a symlink
					t.Fatal(err)
				}
			}
			if tt.foreign {
				setEUID(t, os.Geteuid()+1)
			}
			if err := CheckSocket(path); !matches(err, tt.want) {
				t.Errorf("CheckSocket: %v, want %q", err, tt.want)
			}
			_ = ln.Close()
			ln, err = ListenUnix(path)
			if ln != nil {
				_ = ln.Close()
			}
			if !matches(err, tt.want) {
				t.Errorf("ListenUnix: %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCheckSocket(t *testing.T) {
	dir := shortDir(t)
	if err := CheckSocket(filepath.Join(dir, SocketName)); err != nil {
		t.Errorf("missing socket: %v, want nil so dialing reports no daemon", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSocket(file); !matches(err, "not a socket") {
		t.Errorf("regular file: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckSocket(link); !matches(err, "not a socket") {
		t.Errorf("symlink: %v", err)
	}
}

func matches(err error, want string) bool {
	if want == "" {
		return err == nil
	}
	return errors.Is(err, ErrUntrustedSocket) && strings.Contains(err.Error(), want)
}

func setEUID(t *testing.T, uid int) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = old })
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
