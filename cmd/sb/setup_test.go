package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/platform"
)

// fakeSudo replaces runSudo for the test and records each argv. It never runs sudo.
func fakeSudo(t *testing.T, err error) *[][]string {
	t.Helper()
	var calls [][]string
	orig := runSudo
	runSudo = func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, args)
		return err
	}
	t.Cleanup(func() { runSudo = orig })
	return &calls
}

func runWithInput(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// nssSpy wraps the real platform but records NSS calls instead of making them.
type nssSpy struct {
	platform.Platform
	trusted, untrusted []string
}

func (s *nssSpy) TrustNSS(path string) error   { s.trusted = append(s.trusted, path); return nil }
func (s *nssSpy) UntrustNSS(path string) error { s.untrusted = append(s.untrusted, path); return nil }

// setupEnv isolates tests of setup, uninstall, trust and untrust: macOS as a
// normal user, a temp config dir (so the CA is created there), and no NSS
// changes. System changes only ever go through fakeSudo.
func setupEnv(t *testing.T) (spy *nssSpy, caCert string) {
	t.Helper()
	requireDarwinNonRoot(t)
	dir := configDir(t)
	spy = &nssSpy{}
	orig := currentPlatform
	currentPlatform = func() (platform.Platform, platform.Options) {
		p, o := orig()
		spy.Platform = p
		return spy, o
	}
	t.Cleanup(func() { currentPlatform = orig })
	return spy, filepath.Join(dir, "pki", "ca", "ca.pem")
}

func requireDarwinNonRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("these tests check the macOS plan")
	}
	if os.Geteuid() == 0 {
		t.Skip("setup refuses to run as root")
	}
}

func TestSetupShowsPlanAndAbortsOnNo(t *testing.T) {
	spy, _ := setupEnv(t)
	calls := fakeSudo(t, nil)
	for _, answer := range []string{"n\n", "\n", "", "nope\n"} {
		out, err := runWithInput(t, answer, "setup")
		if err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
		for _, want := range []string{
			"sb setup will make these system changes:",
			"1. Write /etc/resolver/test", "port 15353",
			"/Library/LaunchDaemons/dev.switchboard.helper.plist",
			"Library/LaunchAgents/dev.switchboard.daemon.plist",
			"sudo ", " helper install --uid " + strconv.Itoa(os.Getuid()),
			"/Library/Keychains/System.keychain", "can only sign names under .test", "SHA-256 fingerprint",
			"Continue? [y/N]", "Aborted; nothing was changed.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("answer %q: output missing %q:\n%s", answer, want, out)
			}
		}
	}
	if len(*calls) != 0 || len(spy.trusted) != 0 {
		t.Errorf("changes made without confirmation: sudo %v, NSS %v", *calls, spy.trusted)
	}
}

func TestSetupRunsSingleSudoOnYes(t *testing.T) {
	spy, caCert := setupEnv(t)
	for _, tc := range []struct {
		input string
		args  []string
	}{{"y\n", []string{"setup"}}, {"YES\n", []string{"setup"}}, {"", []string{"setup", "--yes"}}} {
		calls := fakeSudo(t, nil)
		out, err := runWithInput(t, tc.input, tc.args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", tc.args, err, out)
		}
		if len(*calls) != 1 {
			t.Fatalf("%v: sudo calls = %v", tc.args, *calls)
		}
		argv := strings.Join((*calls)[0], " ")
		home, _ := os.UserHomeDir()
		for _, want := range []string{" helper install ", "--uid " + strconv.Itoa(os.Getuid()), "--home " + home, "--sb-path /", "--tld test", "--dns-port 15353", "--ca-cert " + caCert} {
			if !strings.Contains(argv, want) {
				t.Errorf("sudo argv %q missing %q", argv, want)
			}
		}
		if !strings.Contains(out, "sudo "+shellJoin((*calls)[0])) {
			t.Errorf("printed command differs from the one run:\n%s", out)
		}
		if tc.args[len(tc.args)-1] == "--yes" && strings.Contains(out, "Continue?") {
			t.Error("--yes still prompted")
		}
	}
	if _, err := os.Stat(caCert); err != nil {
		t.Errorf("setup did not create the CA: %v", err)
	}
	if len(spy.trusted) != 3 || spy.trusted[0] != caCert {
		t.Errorf("NSS trust calls = %v, want 3 of %s", spy.trusted, caCert)
	}
}

func TestSetupReportsSudoFailure(t *testing.T) {
	spy, _ := setupEnv(t)
	fakeSudo(t, errors.New("exit status 1"))
	_, err := runWithInput(t, "y\n", "setup")
	if err == nil || !strings.Contains(err.Error(), "sb uninstall") {
		t.Errorf("err = %v, want hint to run sb uninstall", err)
	}
	if len(spy.trusted) != 0 {
		t.Errorf("NSS changed after sudo failed: %v", spy.trusted)
	}
}

func TestUninstall(t *testing.T) {
	spy, caCert := setupEnv(t)
	calls := fakeSudo(t, nil)
	out, err := runWithInput(t, "n\n", "uninstall")
	if err != nil || len(*calls) != 0 || !strings.Contains(out, "Aborted") {
		t.Fatalf("decline: err=%v calls=%v\n%s", err, *calls, out)
	}
	for _, want := range []string{"sb uninstall will remove:", "/etc/resolver/test", "dev.switchboard.helper", "routes and the CA files in the config dir are kept"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	out, err = runWithInput(t, "y\n", "uninstall")
	if err != nil || len(*calls) != 1 {
		t.Fatalf("accept: err=%v calls=%v\n%s", err, *calls, out)
	}
	argv := strings.Join((*calls)[0], " ")
	if !strings.Contains(argv, " helper uninstall --uid "+strconv.Itoa(os.Getuid())) || !strings.Contains(argv, "--tld test") {
		t.Errorf("sudo argv = %q", argv)
	}
	if strings.Contains(argv, "--ca-cert") || len(spy.untrusted) != 0 {
		t.Errorf("untrusted a CA that does not exist: argv %q, NSS %v", argv, spy.untrusted)
	}

	// With a CA, uninstall also untrusts it, in the system store and NSS.
	if _, err := ensureCA(); err != nil {
		t.Fatal(err)
	}
	out, err = runWithInput(t, "y\n", "uninstall")
	if err != nil || len(*calls) != 2 {
		t.Fatalf("with CA: err=%v calls=%v\n%s", err, *calls, out)
	}
	if argv := strings.Join((*calls)[1], " "); !strings.HasSuffix(argv, "--ca-cert "+caCert) {
		t.Errorf("sudo argv = %q, want --ca-cert %s", argv, caCert)
	}
	if !strings.Contains(out, "delete it from /Library/Keychains/System.keychain") || strings.Join(spy.untrusted, ",") != caCert {
		t.Errorf("NSS untrust %v; output:\n%s", spy.untrusted, out)
	}
}

func TestTrust(t *testing.T) {
	spy, caCert := setupEnv(t)
	calls := fakeSudo(t, nil)
	out, err := runWithInput(t, "n\n", "trust")
	if err != nil || len(*calls) != 0 || len(spy.trusted) != 0 || !strings.Contains(out, "Aborted") {
		t.Fatalf("decline: err=%v calls=%v nss=%v\n%s", err, *calls, spy.trusted, out)
	}
	for _, want := range []string{"sb trust will:", "1. Add " + caCert + " to /Library/Keychains/System.keychain", "Firefox", "can only sign names under .test"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	out, err = runWithInput(t, "", "trust", "--yes")
	if err != nil || len(*calls) != 1 {
		t.Fatalf("accept: err=%v calls=%v\n%s", err, *calls, out)
	}
	if argv := strings.Join((*calls)[0], " "); !strings.HasSuffix(argv, " helper trust --ca-cert "+caCert) {
		t.Errorf("sudo argv = %q", argv)
	}
	if strings.Join(spy.trusted, ",") != caCert || !strings.Contains(out, "The Switchboard CA is trusted.") {
		t.Errorf("NSS trust %v; output:\n%s", spy.trusted, out)
	}

	fakeSudo(t, errors.New("exit status 1"))
	if _, err := runWithInput(t, "", "trust", "--yes"); err == nil || !strings.Contains(err.Error(), "safe to run 'sb trust' again") {
		t.Errorf("sudo failure: %v", err)
	}
	if len(spy.trusted) != 1 {
		t.Errorf("NSS changed after sudo failed: %v", spy.trusted)
	}
}

func TestUntrust(t *testing.T) {
	spy, caCert := setupEnv(t)
	calls := fakeSudo(t, nil)
	out, err := runWithInput(t, "", "untrust", "--yes")
	if err != nil || len(*calls) != 0 || !strings.Contains(out, "no Switchboard CA") {
		t.Fatalf("no CA: err=%v calls=%v\n%s", err, *calls, out)
	}
	if _, err := os.Stat(caCert); err == nil {
		t.Fatal("untrust created a CA")
	}
	if _, err := ensureCA(); err != nil {
		t.Fatal(err)
	}
	out, err = runWithInput(t, "y\n", "untrust")
	if err != nil || len(*calls) != 1 {
		t.Fatalf("with CA: err=%v calls=%v\n%s", err, *calls, out)
	}
	if argv := strings.Join((*calls)[0], " "); !strings.HasSuffix(argv, " helper untrust --ca-cert "+caCert) {
		t.Errorf("sudo argv = %q", argv)
	}
	if strings.Join(spy.untrusted, ",") != caCert || !strings.Contains(out, "no longer trusted") {
		t.Errorf("NSS untrust %v; output:\n%s", spy.untrusted, out)
	}
	if _, err := os.Stat(caCert); err != nil {
		t.Errorf("untrust deleted the CA files: %v", err)
	}
}

func TestHelperRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	for _, args := range [][]string{
		{"helper", "install", "--uid", "501", "--home", "/Users/x", "--sb-path", "/usr/local/bin/sb"},
		{"helper", "uninstall", "--uid", "501", "--home", "/Users/x"},
		{"helper", "serve", "--uid", "501"},
		{"helper", "trust", "--ca-cert", "/tmp/ca.pem"},
		{"helper", "untrust", "--ca-cert", "/tmp/ca.pem"},
	} {
		_, err := runWithInput(t, "", args...)
		if err == nil || !strings.Contains(err.Error(), "must run as root") {
			t.Errorf("sb %s: err = %v, want root refusal", strings.Join(args, " "), err)
		}
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"/opt/sb", "helper", "--home", "/Users/Jane Doe", "it's"})
	want := `/opt/sb helper --home '/Users/Jane Doe' 'it'\''s'`
	if got != want {
		t.Errorf("shellJoin = %s, want %s", got, want)
	}
}

// fakeAdmin replaces runAdmin for the test and records each command. It never
// shows a password dialog.
func fakeAdmin(t *testing.T, err error) *[][]string {
	t.Helper()
	var calls [][]string
	orig := runAdmin
	runAdmin = func(c *exec.Cmd) error {
		calls = append(calls, c.Args)
		return err
	}
	t.Cleanup(func() { runAdmin = orig })
	return &calls
}

func TestSetupPrintPlan(t *testing.T) {
	spy, caCert := setupEnv(t)
	sudo, admin := fakeSudo(t, nil), fakeAdmin(t, nil)
	out, err := runWithInput(t, "", "setup", "--print-plan")
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Title   string   `json:"title"`
		Changes []string `json:"changes"`
		Command []string `json:"command"`
		Notes   string   `json:"notes"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(plan.Changes) != 6 || plan.Command[1] != "helper" || plan.Command[2] != "install" || !strings.Contains(plan.Notes, "SHA-256") {
		t.Errorf("plan %+v", plan)
	}
	if !strings.Contains(strings.Join(plan.Command, " "), "--ca-cert "+caCert) {
		t.Errorf("command %v", plan.Command)
	}
	if len(*sudo)+len(*admin)+len(spy.trusted) != 0 {
		t.Errorf("--print-plan changed something: sudo %v admin %v nss %v", *sudo, *admin, spy.trusted)
	}
}

func TestSetupWithAdminDialog(t *testing.T) {
	spy, caCert := setupEnv(t)
	sudo, admin := fakeSudo(t, nil), fakeAdmin(t, nil)
	out, err := runWithInput(t, "", "setup", "--yes", "--admin-dialog")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(*sudo) != 0 || len(*admin) != 1 {
		t.Fatalf("sudo %v, admin %v", *sudo, *admin)
	}
	args := (*admin)[0]
	if args[0] != "/usr/bin/osascript" || !strings.Contains(args[2], "'helper' 'install'") ||
		!strings.Contains(args[2], "with administrator privileges") || !strings.Contains(args[2], "Switchboard wants to set up .test names") {
		t.Errorf("osascript args %q", args)
	}
	if !strings.Contains(out, "your system asks for your password") || strings.Contains(out, "sudo ") {
		t.Errorf("output:\n%s", out)
	}
	if len(spy.trusted) != 1 || spy.trusted[0] != caCert {
		t.Errorf("NSS trust after the dialog: %v", spy.trusted)
	}

	// A cancelled dialog is an error, and nothing else happens.
	fakeAdmin(t, errors.New("exit status 1"))
	_, err = runWithInput(t, "", "setup", "--yes", "--admin-dialog")
	if err == nil || !strings.Contains(err.Error(), "cancelled the password dialog") {
		t.Errorf("cancel: %v", err)
	}
	if len(spy.trusted) != 1 {
		t.Errorf("NSS changed after a cancelled dialog: %v", spy.trusted)
	}
}

func TestTrustAndUninstallAcceptAdminDialog(t *testing.T) {
	setupEnv(t)
	fakeSudo(t, nil)
	admin := fakeAdmin(t, nil)
	for _, args := range [][]string{{"trust", "--yes", "--admin-dialog"}, {"untrust", "--yes", "--admin-dialog"}, {"uninstall", "--yes", "--admin-dialog"}} {
		if out, err := runWithInput(t, "", args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if len(*admin) != 3 || !strings.Contains((*admin)[2][2], "'helper' 'uninstall'") {
		t.Errorf("admin calls %q", *admin)
	}
}
