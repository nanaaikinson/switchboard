package darwin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startHelper serves the helper protocol on a short temp socket.
func startHelper(t *testing.T, addrs ...string) *Platform {
	t.Helper()
	dir, err := os.MkdirTemp("", "sbh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := New(Options{UID: os.Getuid(), HelperSocket: filepath.Join(dir, "run", "helper.sock"), HelperAddrs: addrs})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.ServeHelper(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("ServeHelper: %v", err)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if fi, err := os.Stat(p.o.HelperSocket); err == nil && fi.Mode().Perm() == 0o600 {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("helper socket never appeared with mode 0600")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHelperPassesWorkingListeners(t *testing.T) {
	p := startHelper(t, "127.0.0.1:0")
	ctx := context.Background()

	lns, err := p.HelperListeners(ctx)
	if err != nil {
		t.Fatalf("HelperListeners: %v", err)
	}
	if len(lns) != 1 {
		t.Fatalf("got %d listeners", len(lns))
	}
	defer lns[0].Close()

	accepted := make(chan error, 1)
	go func() {
		c, err := lns[0].Accept()
		if err == nil {
			_, err = c.Write([]byte("ok"))
			c.Close()
		}
		accepted <- err
	}()
	c, err := net.DialTimeout("tcp", lns[0].Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial received listener: %v", err)
	}
	buf := make([]byte, 2)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(buf); err != nil || string(buf) != "ok" {
		t.Errorf("read = %q, %v", buf, err)
	}
	c.Close()
	if err := <-accepted; err != nil {
		t.Errorf("accept on received listener: %v", err)
	}

	// A restarted daemon asks again and gets the same bound port.
	again, err := p.HelperListeners(ctx)
	if err != nil {
		t.Fatalf("second HelperListeners: %v", err)
	}
	defer again[0].Close()
	if again[0].Addr().String() != lns[0].Addr().String() {
		t.Errorf("second call bound %s, want same %s", again[0].Addr(), lns[0].Addr())
	}
}

func TestHelperReportsBindError(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	p := startHelper(t, busy.Addr().String())
	_, err = p.HelperListeners(context.Background())
	if err == nil || !strings.Contains(err.Error(), "address already in use") || errors.Is(err, ErrNoHelper) {
		t.Errorf("err = %v, want bind error from helper", err)
	}
}

func TestHelperRejectsBadRequests(t *testing.T) {
	p := startHelper(t, "127.0.0.1:0")
	for req, want := range map[string]string{
		`{"version":99,"op":"listeners"}` + "\n": "protocol version 99",
		`{"version":1,"op":"shell"}` + "\n":      `unknown op "shell"`,
		"garbage\n":                              "bad request",
	} {
		c, err := net.Dial("unix", p.o.HelperSocket)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte(req))
		var resp helperResponse
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := json.NewDecoder(c).Decode(&resp); err != nil {
			t.Fatalf("%q: decode: %v", req, err)
		}
		c.Close()
		if !strings.Contains(resp.Error, want) || len(resp.Addrs) != 0 {
			t.Errorf("%q: resp = %+v, want error containing %q", req, resp, want)
		}
	}
}

func TestHelperListenersWithoutHelper(t *testing.T) {
	p := New(Options{HelperSocket: filepath.Join(t.TempDir(), "missing.sock")})
	if _, err := p.HelperListeners(context.Background()); !errors.Is(err, ErrNoHelper) {
		t.Errorf("err = %v, want ErrNoHelper", err)
	}
}
