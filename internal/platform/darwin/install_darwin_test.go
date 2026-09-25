package darwin

import (
	"bytes"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLaunchctl tracks loaded jobs and records every call. It never runs
// launchctl.
type fakeLaunchctl struct {
	loaded     map[string]bool
	calls      []string
	stuckBoot  bool // bootout "succeeds" but the job stays loaded
	failPrefix string
}

func (f *fakeLaunchctl) run(name string, args ...string) ([]byte, error) {
	if name != "launchctl" {
		return nil, fmt.Errorf("unexpected command %s", name)
	}
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.failPrefix != "" && strings.HasPrefix(call, f.failPrefix) {
		return []byte("boom"), errors.New("exit status 5")
	}
	switch args[0] {
	case "print":
		if !f.loaded[args[1]] {
			return []byte("Could not find service"), errors.New("exit status 113")
		}
	case "bootout":
		if !f.stuckBoot {
			delete(f.loaded, args[1])
		}
	case "bootstrap":
		label := strings.TrimSuffix(filepath.Base(args[2]), ".plist")
		f.loaded[args[1]+"/"+label] = true
	}
	return nil, nil
}

type chownCall struct {
	path     string
	uid, gid int
}

type env struct {
	root string
	p    *Platform
	lc   *fakeLaunchctl
	own  []chownCall
	gid  int
}

// newEnv builds a fake filesystem root with a home dir and an sb binary.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{root: t.TempDir(), lc: &fakeLaunchctl{loaded: map[string]bool{}}}
	mustMkdir(t, filepath.Join(e.root, "Users/me/Library"))
	mustMkdir(t, filepath.Join(e.root, "opt/bin"))
	if err := os.WriteFile(filepath.Join(e.root, "opt/bin/sb"), []byte("#!sb-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"Library/LaunchDaemons", "etc", "var/log", "var/run"} {
		mustMkdir(t, filepath.Join(e.root, d))
	}
	e.p = New(Options{
		UID: os.Getuid(), Home: "/Users/me", SbPath: "/opt/bin/sb", Root: e.root,
		Run: e.lc.run,
		// Tests must never touch the real trust stores.
		Trust: func(*x509.Certificate) error {
			t.Error("unexpected system trust change")
			return errors.New("trust disabled in tests")
		},
		NSS: func() (NSSStore, error) {
			t.Error("unexpected NSS access")
			return nil, errors.New("NSS disabled in tests")
		},
		Chown: func(f *os.File, uid, gid int) error {
			rel, _ := filepath.Rel(e.root, f.Name())
			e.own = append(e.own, chownCall{"/" + rel, uid, gid})
			return nil
		},
	})
	var err error
	if e.gid, err = e.p.userGID(); err != nil {
		t.Fatal(err)
	}
	return e
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func (e *env) read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *env) mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(e.root, p))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func (e *env) exists(p string) bool {
	_, err := os.Lstat(filepath.Join(e.root, p))
	return err == nil
}

func TestResolverContent(t *testing.T) {
	want := "# Managed by Switchboard; removed by 'sb uninstall'\nnameserver 127.0.0.1\nport 15353\n"
	if got := string(resolverContent(15353)); got != want {
		t.Errorf("resolverContent = %q", got)
	}
	m := resolverMarker
	for in, want := range map[string]bool{
		m + "nameserver 127.0.0.1\nport 15353\n": true,
		m + "nameserver 127.0.0.1\nport 53\n":    true, // ours, from an older port
		m + "nameserver 127.0.0.1\nport 15353":   false,
		m + "nameserver 127.0.0.1\nport 0x1\n":   false,
		m + "nameserver 10.0.0.1\nport 53\n":     false,
		"nameserver 127.0.0.1\nport 15353\n":     false, // same settings, no marker
		"nameserver 127.0.0.1\nport 1053\n":      false, // another local DNS tool
		"# valet\nnameserver 127.0.0.1\n":        false,
	} {
		if got := isOurResolver([]byte(in)); got != want {
			t.Errorf("isOurResolver(%q) = %v, want %v", in, got, want)
		}
	}
}

const wantAgentPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>dev.switchboard.daemon</string>
	<key>ProgramArguments</key>
	<array>
		<string>/opt/homebrew/bin/sb</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>/Users/me/Library/Logs/switchboard-daemon.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/me/Library/Logs/switchboard-daemon.log</string>
</dict>
</plist>
`

func TestPlists(t *testing.T) {
	if got := string(agentPlist("/opt/homebrew/bin/sb", "/Users/me/Library/Logs/switchboard-daemon.log")); got != wantAgentPlist {
		t.Errorf("agent plist:\n%s\nwant:\n%s", got, wantAgentPlist)
	}
	helper := string(helperPlist(helperBinPath, 501, helperLogPath))
	for _, want := range []string{
		"<string>dev.switchboard.helper</string>",
		"<string>/Library/PrivilegedHelperTools/dev.switchboard.helper</string>\n\t\t<string>helper</string>\n\t\t<string>serve</string>\n\t\t<string>--uid</string>\n\t\t<string>501</string>",
		"<string>/var/log/switchboard-helper.log</string>",
	} {
		if !strings.Contains(helper, want) {
			t.Errorf("helper plist missing %q:\n%s", want, helper)
		}
	}

	odd := agentPlist("/Users/a&b/<sb>", "/tmp/log")
	if !bytes.Contains(odd, []byte("/Users/a&amp;b/&lt;sb&gt;")) {
		t.Errorf("special characters not escaped:\n%s", odd)
	}
	for name, b := range map[string][]byte{"agent": []byte(wantAgentPlist), "helper": []byte(helper), "odd": odd} {
		lintPlist(t, name, b)
	}
}

// lintPlist checks well-formed XML, and runs the read-only plutil -lint when
// it is available.
func lintPlist(t *testing.T, name string, b []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		if _, err := dec.Token(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("%s plist is not well-formed XML: %v", name, err)
		}
	}
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		return
	}
	f := filepath.Join(t.TempDir(), name+".plist")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", f).CombinedOutput(); err != nil {
		t.Errorf("plutil -lint %s: %v: %s", name, err, out)
	}
}

func TestInstallResolver(t *testing.T) {
	e := newEnv(t)
	for range 2 { // second run is a no-op
		if err := e.p.InstallResolver("test", 15353); err != nil {
			t.Fatalf("InstallResolver: %v", err)
		}
	}
	if got := e.read(t, "/etc/resolver/test"); got != string(resolverContent(15353)) {
		t.Errorf("content = %q", got)
	}
	if m := e.mode(t, "/etc/resolver/test"); m != 0o644 {
		t.Errorf("mode = %o", m)
	}
	if len(e.own) == 0 || e.own[0].uid != 0 || e.own[0].gid != 0 {
		t.Errorf("resolver not chowned to root: %+v", e.own)
	}

	foreign := "nameserver 127.0.0.1\nport 53\n# valet\n"
	if err := os.WriteFile(filepath.Join(e.root, "etc/resolver/other"), []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.p.InstallResolver("other", 15353); !errors.Is(err, ErrForeignFile) {
		t.Errorf("foreign resolver err = %v, want ErrForeignFile", err)
	}
	if got := e.read(t, "/etc/resolver/other"); got != foreign {
		t.Error("foreign resolver was modified")
	}
	for _, bad := range []string{"", "../etc", "Test", "a.b", "-x"} {
		if err := e.p.InstallResolver(bad, 15353); err == nil {
			t.Errorf("InstallResolver(%q) succeeded", bad)
		}
	}
}

func TestRemoveResolver(t *testing.T) {
	e := newEnv(t)
	if err := e.p.RemoveResolver("test"); err != nil {
		t.Fatalf("remove when nothing installed: %v", err)
	}
	if err := e.p.InstallResolver("test", 15353); err != nil {
		t.Fatal(err)
	}
	if err := e.p.RemoveResolver("test"); err != nil {
		t.Fatal(err)
	}
	if e.exists("/etc/resolver/test") || e.exists("/etc/resolver") {
		t.Error("resolver file or empty dir left behind")
	}

	// Another tool's file for the same TLD is kept, even when it also points
	// at 127.0.0.1, and so is the directory.
	mustMkdir(t, filepath.Join(e.root, "etc/resolver"))
	for _, foreign := range []string{"nameserver 10.0.0.1\n", "nameserver 127.0.0.1\nport 1053\n", "nameserver 127.0.0.1\nport 15353\n"} {
		if err := os.WriteFile(filepath.Join(e.root, "etc/resolver/test"), []byte(foreign), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := e.p.RemoveResolver("test"); !errors.Is(err, ErrForeignFile) {
			t.Errorf("%q: err = %v, want ErrForeignFile", foreign, err)
		}
		if e.read(t, "/etc/resolver/test") != foreign {
			t.Errorf("%q: foreign resolver deleted or changed", foreign)
		}
	}
}

func TestInstallService(t *testing.T) {
	e := newEnv(t)
	if err := e.p.InstallService(); err != nil {
		t.Fatalf("InstallService: %v", err)
	}
	uid := os.Getuid()
	agent := "/Users/me/Library/LaunchAgents/dev.switchboard.daemon.plist"

	if got := e.read(t, helperBinPath); got != "#!sb-binary" {
		t.Errorf("helper binary = %q", got)
	}
	checks := []struct {
		path string
		mode os.FileMode
		want string
	}{
		{helperBinPath, 0o755, ""},
		{helperPlistPath, 0o644, string(helperPlist(helperBinPath, uid, helperLogPath))},
		{agent, 0o644, string(agentPlist("/opt/bin/sb", "/Users/me/Library/Logs/switchboard-daemon.log"))},
	}
	for _, c := range checks {
		if m := e.mode(t, c.path); m != c.mode {
			t.Errorf("%s mode = %o, want %o", c.path, m, c.mode)
		}
		if c.want != "" && e.read(t, c.path) != c.want {
			t.Errorf("%s content mismatch:\n%s", c.path, e.read(t, c.path))
		}
	}

	wantOwners := map[string][2]int{
		helperBinPath:                    {0, 0},
		helperPlistPath:                  {0, 0},
		agent:                            {uid, e.gid},
		"/Users/me/Library/LaunchAgents": {uid, e.gid},
	}
	for _, c := range e.own {
		dir := filepath.Dir(c.path)
		key := c.path
		if strings.HasPrefix(filepath.Base(c.path), ".") { // temp file renamed into place
			key = filepath.Join(dir, strings.SplitN(strings.TrimPrefix(filepath.Base(c.path), "."), ".sb-", 2)[0])
		}
		if want, ok := wantOwners[key]; ok {
			if c.uid != want[0] || c.gid != want[1] {
				t.Errorf("%s owned by %d:%d, want %d:%d", key, c.uid, c.gid, want[0], want[1])
			}
			delete(wantOwners, key)
		}
	}
	if len(wantOwners) > 0 {
		t.Errorf("never chowned: %v", wantOwners)
	}

	wantCalls := []string{
		"print system/dev.switchboard.helper",
		"bootstrap system " + helperPlistPath,
		fmt.Sprintf("print gui/%d/dev.switchboard.daemon", uid),
		fmt.Sprintf("bootstrap gui/%d %s", uid, agent),
	}
	if strings.Join(e.lc.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("launchctl calls:\n%s\nwant:\n%s", strings.Join(e.lc.calls, "\n"), strings.Join(wantCalls, "\n"))
	}

	// Re-running boots out the loaded jobs, then bootstraps them again.
	e.lc.calls = nil
	if err := e.p.InstallService(); err != nil {
		t.Fatalf("second InstallService: %v", err)
	}
	if n := strings.Count(strings.Join(e.lc.calls, "\n"), "bootout"); n != 2 {
		t.Errorf("reinstall bootouts = %d, calls:\n%s", n, strings.Join(e.lc.calls, "\n"))
	}
	if len(e.lc.loaded) != 2 {
		t.Errorf("loaded = %v", e.lc.loaded)
	}
}

func TestInstallServiceRefusesSymlinkedUserDirs(t *testing.T) {
	for _, link := range []string{"Users/me/Library/LaunchAgents", "Users/me/Library"} {
		t.Run(link, func(t *testing.T) {
			e := newEnv(t)
			target := filepath.Join(e.root, "Library/LaunchDaemons")
			full := filepath.Join(e.root, link)
			if err := os.RemoveAll(full); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, full); err != nil {
				t.Fatal(err)
			}
			if err := e.p.InstallService(); err == nil || !strings.Contains(err.Error(), "must be a directory") {
				t.Fatalf("err = %v, want refusal", err)
			}
			if e.exists("/Library/LaunchDaemons/dev.switchboard.daemon.plist") {
				t.Error("agent plist written through symlink into LaunchDaemons")
			}
		})
	}
}

func TestInstallServiceBootstrapFailure(t *testing.T) {
	e := newEnv(t)
	e.lc.failPrefix = "bootstrap system"
	if err := e.p.InstallService(); err == nil || !strings.Contains(err.Error(), "launchctl bootstrap system") {
		t.Errorf("err = %v", err)
	}
}

func TestRemoveService(t *testing.T) {
	e := newEnv(t)
	if err := e.p.RemoveService(); err != nil {
		t.Fatalf("remove before install: %v", err)
	}
	if err := e.p.InstallService(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{helperLogPath, "/Users/me/Library/Logs/switchboard-daemon.log", DefaultHelperSocket} {
		mustMkdir(t, filepath.Dir(filepath.Join(e.root, f)))
		if err := os.WriteFile(filepath.Join(e.root, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	e.lc.calls = nil
	if err := e.p.RemoveService(); err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	for _, f := range []string{
		helperBinPath, helperPlistPath, helperLogPath, helperSockDir,
		"/Users/me/Library/LaunchAgents/dev.switchboard.daemon.plist",
		"/Users/me/Library/Logs/switchboard-daemon.log",
	} {
		if e.exists(f) {
			t.Errorf("%s still exists", f)
		}
	}
	if len(e.lc.loaded) != 0 {
		t.Errorf("still loaded: %v", e.lc.loaded)
	}
	if !e.exists("/Users/me/Library/LaunchAgents") {
		t.Error("removed the user's LaunchAgents directory")
	}

	// Idempotent: a second run only probes launchctl.
	e.lc.calls = nil
	if err := e.p.RemoveService(); err != nil {
		t.Fatalf("second RemoveService: %v", err)
	}
	for _, c := range e.lc.calls {
		if !strings.HasPrefix(c, "print ") {
			t.Errorf("second uninstall ran %q", c)
		}
	}
}

func TestRemoveServiceReportsStuckJob(t *testing.T) {
	e := newEnv(t)
	if err := e.p.InstallService(); err != nil {
		t.Fatal(err)
	}
	e.lc.stuckBoot = true
	if err := e.p.RemoveService(); err == nil || !strings.Contains(err.Error(), "still loaded") {
		t.Errorf("err = %v, want still loaded", err)
	}
}

func TestValidate(t *testing.T) {
	e := newEnv(t)
	if err := e.p.Validate(); err != nil {
		t.Fatalf("valid options: %v", err)
	}
	if err := os.Symlink(filepath.Join(e.root, "Users/me"), filepath.Join(e.root, "Users/link")); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Options){
		"root uid":      func(o *Options) { o.UID = 0 },
		"relative home": func(o *Options) { o.Home = "Users/me" },
		"missing home":  func(o *Options) { o.Home = "/Users/nobody" },
		"symlink home":  func(o *Options) { o.Home = "/Users/link" },
		"missing sb":    func(o *Options) { o.SbPath = "/opt/bin/nope" },
		"sb is dir":     func(o *Options) { o.SbPath = "/opt/bin" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			o := e.p.o
			mutate(&o)
			if err := New(o).Validate(); err == nil {
				t.Error("Validate succeeded")
			}
		})
	}
}

func TestPlans(t *testing.T) {
	e := newEnv(t)
	install := strings.Join(e.p.InstallPlan("test", 15353), "\n")
	for _, want := range []string{"/etc/resolver/test", "port 15353", "/opt/bin/sb", helperBinPath, helperPlistPath, "ports 80 and 443",
		"/Users/me/Library/LaunchAgents/dev.switchboard.daemon.plist"} {
		if !strings.Contains(install, want) {
			t.Errorf("install plan missing %q:\n%s", want, install)
		}
	}
	uninstall := strings.Join(e.p.UninstallPlan("test"), "\n")
	for _, want := range []string{"/etc/resolver/test", helperBinPath, helperPlistPath, helperLogPath, helperSockDir, "dev.switchboard.daemon.plist"} {
		if !strings.Contains(uninstall, want) {
			t.Errorf("uninstall plan missing %q:\n%s", want, uninstall)
		}
	}
}
