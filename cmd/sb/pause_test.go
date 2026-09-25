package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/api/client"
)

func TestPauseAndResume(t *testing.T) {
	dir := configDir(t)
	startDaemon(t, daemonOptions{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("app")) }))
	defer up.Close()
	mustRun(t, "add", "myapp", strconv.Itoa(up.Listener.Addr().(*net.TCPAddr).Port))
	st := status(t, dir)
	c := httpsClient(t, dir, st.HTTPS.Addrs[0])
	get := func() (int, string) {
		t.Helper()
		resp, err := c.Get("https://myapp.test/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Watch the event stream for paused.changed.
	events := make(chan string, 8)
	httpc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return client.Dial(ctx, controlAddr())
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://sb/v1/events", nil)
	resp, err := httpc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			// Only pause events; health checks send their own at any time.
			if strings.HasPrefix(sc.Text(), "data: ") && strings.Contains(sc.Text(), `"type":"paused.changed"`) {
				events <- sc.Text()
			}
		}
	}()

	if code, body := get(); code != 200 || body != "app" {
		t.Fatalf("before pause: %d %q", code, body)
	}
	if out := mustRun(t, "pause"); !strings.Contains(out, "Paused") {
		t.Errorf("pause: %q", out)
	}
	if code, body := get(); code != http.StatusServiceUnavailable || !strings.Contains(body, "Switchboard is paused") {
		t.Errorf("paused: %d %q", code, body)
	}
	if !status(t, dir).Paused {
		t.Error("status not paused")
	}
	if out := mustRun(t, "ls"); !strings.Contains(out, "Switchboard is paused") {
		t.Errorf("ls doesn't warn:\n%s", out)
	}
	select {
	case e := <-events:
		if !strings.Contains(e, `"type":"paused.changed"`) || !strings.Contains(e, `"paused":true`) {
			t.Errorf("event %s", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no paused.changed event")
	}
	mustRun(t, "pause") // again: no second event
	mustRun(t, "resume")
	select {
	case e := <-events:
		if !strings.Contains(e, `"paused":false`) {
			t.Errorf("event %s, want resume (and no duplicate pause event)", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no resume event")
	}
	if code, body := get(); code != 200 || body != "app" {
		t.Errorf("after resume: %d %q", code, body)
	}
}
