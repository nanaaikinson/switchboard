package windows

import (
	"net"
	"sync"
)

// closableListener makes Close return at once and a pending Accept return
// net.ErrClosed, whatever the wrapped listener does. go-winio's pipe listener
// can block forever in Close: if the pending client connection fails with an
// unexpected error just as Close aborts it, its listener loop swallows the one
// close signal and keeps waiting for another, and Close waits for the loop.
// http.Server.Shutdown calls Close while holding its lock, so that would hang
// the daemon's shutdown. The wrapped listener is closed in the background;
// if its Close hangs, only that goroutine leaks, until the process exits.
type closableListener struct {
	net.Listener
	once   sync.Once
	closed chan struct{}
}

func newClosableListener(ln net.Listener) *closableListener {
	return &closableListener{Listener: ln, closed: make(chan struct{})}
}

type accepted struct {
	c   net.Conn
	err error
}

// Accept waits for the next connection, or for Close.
func (l *closableListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	default:
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := l.Listener.Accept()
		ch <- accepted{c, err}
	}()
	select {
	case a := <-ch:
		return a.c, a.err
	case <-l.closed:
		// A connection that arrives after Close is dropped.
		go func() {
			if a := <-ch; a.c != nil {
				_ = a.c.Close()
			}
		}()
		return nil, net.ErrClosed
	}
}

// Close stops Accept and closes the wrapped listener in the background.
func (l *closableListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		go func() { _ = l.Listener.Close() }()
	})
	return nil
}
