package windows

import (
	"errors"
	"net"
	"testing"
	"time"
)

// stuckListener is a listener whose Accept and Close never return, as
// go-winio's can after its close signal is lost.
type stuckListener struct {
	net.Listener // nil: only Accept, Close and Addr are used
	block        chan struct{}
}

func (s stuckListener) Accept() (net.Conn, error) { <-s.block; return nil, errors.New("unreachable") }
func (s stuckListener) Close() error              { <-s.block; return nil }
func (s stuckListener) Addr() net.Addr            { return &net.UnixAddr{Name: "stuck", Net: "unix"} }

func TestClosableListenerNeverHangs(t *testing.T) {
	inner := stuckListener{block: make(chan struct{})}
	defer close(inner.block)
	ln := newClosableListener(inner)

	accepted := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		accepted <- err
	}()
	time.Sleep(20 * time.Millisecond) // let Accept start waiting

	closed := make(chan struct{})
	go func() { _ = ln.Close(); _ = ln.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on the wrapped listener")
	}
	select {
	case err := <-accepted:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("pending Accept = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending Accept didn't return after Close")
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept after Close = %v, want net.ErrClosed", err)
	}
}

func TestClosableListenerPassesConnections(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newClosableListener(tcp)
	defer ln.Close()
	go func() {
		if c, err := net.Dial("tcp", tcp.Addr().String()); err == nil {
			_ = c.Close()
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	_ = ln.Close()
	if _, err := net.Dial("tcp", tcp.Addr().String()); err == nil {
		// The inner Close runs in the background; give it a moment.
		time.Sleep(100 * time.Millisecond)
		if c, err := net.Dial("tcp", tcp.Addr().String()); err == nil {
			_ = c.Close()
			t.Error("inner listener still accepts after Close")
		}
	}
}
