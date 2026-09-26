//go:build darwin || linux

package client

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUID(t *testing.T) {
	dir, err := os.MkdirTemp("", "sbp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer c.Close()
			_, _ = c.Read(make([]byte, 1))
		}
	}()
	c, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if uid, err := peerUID(c.(*net.UnixConn)); err != nil || uid != os.Geteuid() {
		t.Errorf("peerUID = %d, %v; want %d", uid, err, os.Geteuid())
	}
}
