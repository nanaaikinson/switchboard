package darwin

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// dnssdMsg is one request the fake mDNSResponder received.
type dnssdMsg struct {
	op, ctx, index uint32
	data           []byte
	gotFD          bool
}

// fakeDNSSD speaks the server side of mDNSResponder's protocol: it answers
// the connection request on the connection, and record requests on the
// socket passed with the last byte. result is sent for record requests.
type fakeDNSSD struct {
	mu     sync.Mutex
	msgs   []dnssdMsg
	result int32
	conns  []*net.UnixConn
}

func startFakeDNSSD(t *testing.T) (string, *fakeDNSSD) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dnssd") // short: socket paths are limited to 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDNSSD{}
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			go f.serve(t, c)
		}
	}()
	return path, f
}

func (f *fakeDNSSD) serve(t *testing.T, c *net.UnixConn) {
	for {
		hdr := make([]byte, dnssdHeaderLen)
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		m := dnssdMsg{
			op:    binary.BigEndian.Uint32(hdr[12:]),
			ctx:   binary.BigEndian.Uint32(hdr[16:]),
			index: binary.BigEndian.Uint32(hdr[24:]),
		}
		if v := binary.BigEndian.Uint32(hdr[0:]); v != dnssdVersion {
			t.Errorf("version %d", v)
		}
		n := int(binary.BigEndian.Uint32(hdr[4:]))
		if m.op == opConnection {
			_, _ = c.Write(be32(0))
			f.record(m)
			continue
		}
		// Record ops: all but the last byte, then the last byte with an fd.
		m.data = make([]byte, n)
		if _, err := io.ReadFull(c, m.data[:n-1]); err != nil {
			return
		}
		oob := make([]byte, syscall.CmsgSpace(4))
		_, oobn, _, _, err := c.ReadMsgUnix(m.data[n-1:], oob)
		if err != nil {
			return
		}
		if scms, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil && len(scms) == 1 {
			if fds, err := syscall.ParseUnixRights(&scms[0]); err == nil && len(fds) == 1 {
				m.gotFD = true
				f.mu.Lock()
				res := f.result
				f.mu.Unlock()
				_, _ = syscall.Write(fds[0], be32(uint32(res)))
				_ = syscall.Close(fds[0])
			}
		}
		f.record(m)
	}
}

func (f *fakeDNSSD) record(m dnssdMsg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
}

func (f *fakeDNSSD) take() []dnssdMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.msgs
	f.msgs = nil
	return out
}

func TestDNSSDRegistersAndRemoves(t *testing.T) {
	path, f := startFakeDNSSD(t)
	d, err := openDNSSD(path)
	if err != nil {
		t.Fatalf("openDNSSD: %v", err)
	}
	defer d.Close()
	if msgs := f.take(); len(msgs) != 1 || msgs[0].op != opConnection {
		t.Fatalf("after open: %+v, want one connection request", msgs)
	}

	if err := d.Set([]string{"myapp.local"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	msgs := f.take()
	if len(msgs) != 2 {
		t.Fatalf("got %d requests, want A and AAAA registrations", len(msgs))
	}
	for i, want := range []struct {
		typ   uint16
		rdata []byte
	}{{1, []byte{127, 0, 0, 1}}, {28, net.IPv6loopback}} {
		m := msgs[i]
		body := registerBody("myapp.local", want.typ, want.rdata)
		if m.op != opRegisterRecord || !m.gotFD || m.index != uint32(i) || m.ctx == 0 {
			t.Errorf("request %d = %+v, want reg_record with index %d, a context and a result socket", i, m, i)
		}
		if string(m.data) != "\x00"+string(body) {
			t.Errorf("request %d body = %q, want %q", i, m.data, body)
		}
	}
	if b := msgs[0].data[1:]; binary.BigEndian.Uint32(b[4:]) != ifLocalOnly || !strings.Contains(string(b), "myapp.local.\x00") {
		t.Errorf("record not local-only or misnamed: %q", b)
	}

	// Unchanged names aren't touched; removed ones are withdrawn by index.
	if err := d.Set([]string{"myapp.local", "api.local"}); err != nil {
		t.Fatal(err)
	}
	if msgs := f.take(); len(msgs) != 2 || msgs[0].index != 2 || msgs[1].index != 3 {
		t.Errorf("adding api.local sent %+v, want 2 registrations (indexes 2, 3)", msgs)
	}
	if err := d.Set([]string{"api.local"}); err != nil {
		t.Fatal(err)
	}
	msgs = f.take()
	if len(msgs) != 2 || msgs[0].op != opRemoveRecord || msgs[1].op != opRemoveRecord || msgs[0].index+msgs[1].index != 1 || msgs[0].ctx != 0 {
		t.Errorf("removing myapp.local sent %+v, want removals of indexes 0 and 1", msgs)
	}
}

func TestDNSSDReportsErrors(t *testing.T) {
	path, f := startFakeDNSSD(t)
	d, err := openDNSSD(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f.mu.Lock()
	f.result = -65570
	f.mu.Unlock()
	err = d.Set([]string{"myapp.local"})
	if err == nil || !strings.Contains(err.Error(), "announce myapp.local: denied by policy") {
		t.Fatalf("err = %v", err)
	}
	if len(d.regs) != 0 {
		t.Errorf("failed name kept: %v", d.regs)
	}
}

func TestDNSSDLost(t *testing.T) {
	path, f := startFakeDNSSD(t)
	d, err := openDNSSD(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f.mu.Lock()
	for _, c := range f.conns {
		_ = c.Close() // mDNSResponder restarts
	}
	f.mu.Unlock()
	select {
	case <-d.Lost():
	case <-time.After(2 * time.Second):
		t.Fatal("Lost not closed after the connection dropped")
	}
	if err := d.Set([]string{"a.local"}); err == nil {
		t.Error("Set succeeded on a lost connection")
	}
}

func TestOpenDNSSDNotRunning(t *testing.T) {
	_, err := openDNSSD(filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "connect to mDNSResponder") {
		t.Fatalf("err = %v", err)
	}
}
