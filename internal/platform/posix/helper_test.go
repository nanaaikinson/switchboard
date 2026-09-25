//go:build darwin || linux

package posix

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// startHelper serves the helper protocol on a short temp socket.
func startHelper(t *testing.T, hosts func([]string) error, addrs ...string) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "sbh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &Server{Socket: filepath.Join(dir, "run", "helper.sock"), UID: os.Getuid(), GID: os.Getgid(), Addrs: addrs, Hosts: hosts}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if fi, err := os.Stat(s.Socket); err == nil && fi.Mode().Perm() == 0o600 {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatal("helper socket never appeared with mode 0600")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHelperPassesWorkingListeners(t *testing.T) {
	s := startHelper(t, nil, "127.0.0.1:0")
	ctx := context.Background()

	lns, err := Listeners(ctx, s.Socket)
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
	again, err := Listeners(ctx, s.Socket)
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
	s := startHelper(t, nil, busy.Addr().String())
	_, err = Listeners(context.Background(), s.Socket)
	if err == nil || !strings.Contains(err.Error(), "address already in use") || errors.Is(err, ErrNoHelper) {
		t.Errorf("err = %v, want bind error from helper", err)
	}
}

func TestHelperRejectsBadRequests(t *testing.T) {
	s := startHelper(t, nil, "127.0.0.1:0")
	for req, want := range map[string]string{
		`{"version":99,"op":"listeners"}` + "\n": "protocol version 99",
		`{"version":1,"op":"shell"}` + "\n":      `unknown op "shell"`,
		`{"version":1,"op":"hosts"}` + "\n":      `unknown op "hosts"`, // no Hosts func
		"garbage\n":                              "bad request",
	} {
		c, err := net.Dial("unix", s.Socket)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte(req))
		var resp Response
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
	sock := filepath.Join(t.TempDir(), "missing.sock")
	if _, err := Listeners(context.Background(), sock); !errors.Is(err, ErrNoHelper) {
		t.Errorf("err = %v, want ErrNoHelper", err)
	}
	if err := SyncHosts(context.Background(), sock, nil); !errors.Is(err, ErrNoHelper) {
		t.Errorf("SyncHosts err = %v, want ErrNoHelper", err)
	}
}

func TestHelperHostsOp(t *testing.T) {
	var mu sync.Mutex
	var got [][]string
	s := startHelper(t, func(names []string) error {
		if len(names) > 0 && names[0] == "bad" {
			return errors.New("refused bad")
		}
		mu.Lock()
		got = append(got, names)
		mu.Unlock()
		return nil
	})
	if err := SyncHosts(context.Background(), s.Socket, []string{"myapp.test", "probe.test"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(got) != 1 || strings.Join(got[0], ",") != "myapp.test,probe.test" {
		t.Errorf("hosts got %v", got)
	}
	mu.Unlock()
	if err := SyncHosts(context.Background(), s.Socket, []string{"bad"}); err == nil || !strings.Contains(err.Error(), "refused bad") {
		t.Errorf("err = %v", err)
	}
}

func TestHelperRejectsHugeRequest(t *testing.T) {
	s := startHelper(t, func([]string) error { return nil })
	c, err := net.Dial("unix", s.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() {
		_, _ = c.Write(append([]byte(`{"version":1,"op":"hosts","names":["`), make([]byte, 2*maxRequest)...))
	}()
	var resp Response
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.Error, "bad request") {
		t.Errorf("resp = %+v, want bad request", resp)
	}
}
