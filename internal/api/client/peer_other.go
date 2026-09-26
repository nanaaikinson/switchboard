//go:build !windows && !darwin && !linux

package client

import (
	"errors"
	"net"
)

// peerUID is not implemented here; Dial relies on the path checks.
func peerUID(*net.UnixConn) (int, error) { return -1, errors.ErrUnsupported }
