//go:build !windows

package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/api/client"
)

// A socket in a directory others can write to may have been replaced by
// someone posing as the daemon: the CLI refuses it and says how to fix it.
func TestClientRefusesUntrustedSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, api.SocketName)
	ln, err := api.ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err = client.New(sock).Status(context.Background())
	if !errors.Is(err, api.ErrUntrustedSocket) || !strings.HasPrefix(err.Error(), "untrusted control socket: ") || !strings.Contains(err.Error(), "chmod 700 "+dir) {
		t.Errorf("err = %v", err)
	}
	if errors.Is(err, client.ErrDaemonNotRunning) {
		t.Error("reported as a daemon that isn't running")
	}
}
