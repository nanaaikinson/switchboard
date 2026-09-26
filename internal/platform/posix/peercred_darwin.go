package posix

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the uid of the process at the other end of c.
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, serr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		if cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); serr == nil {
			uid = int(cred.Uid)
		}
	}); err != nil {
		return -1, err
	}
	return uid, serr
}
