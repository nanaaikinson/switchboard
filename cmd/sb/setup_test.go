package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
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

func requireDarwinNonRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("sb setup is macOS-only")
	}
	if os.Geteuid() == 0 {
		t.Skip("setup refuses to run as root")
	}
}

func TestSetupShowsPlanAndAbortsOnNo(t *testing.T) {
	requireDarwinNonRoot(t)
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
			"Continue? [y/N]", "Aborted; nothing was changed.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("answer %q: output missing %q:\n%s", answer, want, out)
			}
		}
	}
	if len(*calls) != 0 {
		t.Errorf("sudo ran without confirmation: %v", *calls)
	}
}

func TestSetupRunsSingleSudoOnYes(t *testing.T) {
	requireDarwinNonRoot(t)
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
		for _, want := range []string{" helper install ", "--uid " + strconv.Itoa(os.Getuid()), "--home " + home, "--sb-path /", "--tld test", "--dns-port 15353"} {
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
}

func TestSetupReportsSudoFailure(t *testing.T) {
	requireDarwinNonRoot(t)
	fakeSudo(t, errors.New("exit status 1"))
	_, err := runWithInput(t, "y\n", "setup")
	if err == nil || !strings.Contains(err.Error(), "sb uninstall") {
		t.Errorf("err = %v, want hint to run sb uninstall", err)
	}
}

func TestUninstall(t *testing.T) {
	requireDarwinNonRoot(t)
	calls := fakeSudo(t, nil)
	out, err := runWithInput(t, "n\n", "uninstall")
	if err != nil || len(*calls) != 0 || !strings.Contains(out, "Aborted") {
		t.Fatalf("decline: err=%v calls=%v\n%s", err, *calls, out)
	}
	for _, want := range []string{"sb uninstall will remove:", "/etc/resolver/test", "dev.switchboard.helper", "routes in the config dir are kept"} {
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
}

func TestHelperRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	for _, args := range [][]string{
		{"helper", "install", "--uid", "501", "--home", "/Users/x", "--sb-path", "/usr/local/bin/sb"},
		{"helper", "uninstall", "--uid", "501", "--home", "/Users/x"},
		{"helper", "serve", "--uid", "501"},
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
