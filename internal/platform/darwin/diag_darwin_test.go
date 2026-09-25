package darwin

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixOf(err error) string {
	var f interface{ Fix() string }
	if errors.As(err, &f) {
		return f.Fix()
	}
	return ""
}

func TestCheckResolver(t *testing.T) {
	tests := []struct {
		name     string
		content  string // "" means no file
		wantErr  string
		wantFix  string
		wantPass bool
	}{
		{name: "ours", content: string(resolverContent(15353)), wantPass: true},
		{name: "missing", wantErr: "/etc/resolver/test is missing", wantFix: "Run 'sb setup'."},
		{name: "ours, wrong port", content: string(resolverContent(1053)),
			wantErr: "points at the wrong port (1053), want 15353", wantFix: "Run 'sb uninstall', then 'sb setup'."},
		{name: "foreign", content: "nameserver 127.0.0.1\nport 1053\n",
			wantErr: "written by another tool (nameserver 127.0.0.1 port 1053)", wantFix: "Another local DNS tool owns .test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.content != "" {
				mustMkdir(t, filepath.Join(e.root, "etc/resolver"))
				if err := os.WriteFile(filepath.Join(e.root, "etc/resolver/test"), []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := e.p.CheckResolver("test", 15353)
			if tt.wantPass {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if fix := fixOf(err); !strings.HasPrefix(fix, tt.wantFix) {
				t.Errorf("fix = %q, want prefix %q", fix, tt.wantFix)
			}
		})
	}
	if err := newEnv(t).p.CheckResolver("../etc", 1); err == nil {
		t.Error("invalid tld accepted")
	}
}

func TestHelperRunning(t *testing.T) {
	// Unix socket paths are limited to 104 bytes, so avoid t.TempDir.
	dir, err := os.MkdirTemp("", "sbh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "helper.sock")
	e := newEnv(t)
	e.p.o.HelperSocket = sock
	ctx := context.Background()

	err = e.p.HelperRunning(ctx)
	if err == nil || !strings.Contains(err.Error(), "not installed") || fixOf(err) != "Run 'sb setup'." {
		t.Errorf("no plist: err = %v, fix %q", err, fixOf(err))
	}

	if err := os.WriteFile(e.p.fs(helperPlistPath), []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.p.HelperRunning(ctx); err == nil || !strings.Contains(err.Error(), "installed but not running") {
		t.Errorf("no socket: err = %v", err)
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.p.HelperRunning(ctx); err != nil {
		t.Errorf("listening: err = %v", err)
	}

	// A socket file with no listener behind it refuses connections.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if err := e.p.HelperRunning(ctx); err == nil || !strings.Contains(err.Error(), "not accepting connections") {
		t.Errorf("stale socket: err = %v", err)
	}
}

func TestLookupHost(t *testing.T) {
	out := "name: probe.test\nipv6_address: ::1\n\nname: probe.test\nip_address: 127.0.0.1\n\n"
	var got []string
	p := New(Options{Run: func(name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte(out), nil
	}})
	addrs, err := p.LookupHost(context.Background(), "probe.test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(addrs, ",") != "::1,127.0.0.1" {
		t.Errorf("addrs = %v", addrs)
	}
	if strings.Join(got, " ") != "dscacheutil -q host -a name probe.test" {
		t.Errorf("ran %v", got)
	}

	p = New(Options{Run: func(string, ...string) ([]byte, error) { return []byte("bad"), errors.New("exit status 1") }})
	if _, err := p.LookupHost(context.Background(), "probe.test"); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Errorf("err = %v", err)
	}
}

func TestPortOwner(t *testing.T) {
	exitErr := exec.Command("false").Run()
	tests := []struct {
		name    string
		out     string
		err     error
		want    string
		wantErr bool
	}{
		{name: "one process", out: "p123\ncnginx\nf6\n", want: "nginx (pid 123)"},
		{name: "several, first wins", out: "p1\nchttpd\np2\ncnode\n", want: "httpd (pid 1)"},
		{name: "none", err: exitErr, want: ""},
		{name: "lsof missing", err: exec.ErrNotFound, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var args []string
			p := New(Options{Run: func(name string, a ...string) ([]byte, error) {
				args = append([]string{name}, a...)
				return []byte(tt.out), tt.err
			}})
			got, err := p.PortOwner(context.Background(), 80)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("PortOwner = %q, %v; want %q, err %v", got, err, tt.want, tt.wantErr)
			}
			if strings.Join(args, " ") != "lsof -nP -iTCP:80 -sTCP:LISTEN -Fpc" {
				t.Errorf("ran %v", args)
			}
		})
	}
}
