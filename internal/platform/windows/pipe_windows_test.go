package windows

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testPipe(t *testing.T) string {
	t.Helper()
	sid, err := CurrentSID()
	if err != nil {
		t.Fatal(err)
	}
	return PipeName(sid, fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()))
}

func TestPipeRoundTrip(t *testing.T) {
	name := testPipe(t)
	ln, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		_, _ = c.Write([]byte("echo " + line))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := DialPipe(ctx, name) // also checks the server runs as us
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("hi\n"))
	got, _ := bufio.NewReader(c).ReadString('\n')
	if strings.TrimSpace(got) != "echo hi" {
		t.Errorf("got %q", got)
	}
}

func TestPipeCantBeTakenTwice(t *testing.T) {
	name := testPipe(t)
	ln, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// A second server (a squatter, or a second daemon) is refused.
	if ln2, err := ListenPipe(name); err == nil {
		ln2.Close()
		t.Fatal("second ListenPipe succeeded")
	}
}

func TestDialMissingPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := DialPipe(ctx, testPipe(t)); !errors.Is(err, ErrPipeNotFound) {
		t.Errorf("err = %v, want ErrPipeNotFound", err)
	}
}
