package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr error
		errText string
	}{
		{name: "helper", args: []string{"helper"}, wantErr: errNotImplemented},
		{name: "doctor", args: []string{"doctor"}, wantErr: errNotImplemented},
		{name: "add missing port", args: []string{"add", "myapp"}, errText: "accepts 2 arg(s)"},
		{name: "add non-numeric port", args: []string{"add", "myapp", "http"}, errText: "is not a number"},
		{name: "daemon extra arg", args: []string{"daemon", "x"}, errText: "unknown command"},
		{name: "ls extra arg", args: []string{"ls", "x"}, errText: "unknown command"},
		{name: "unknown command", args: []string{"frobnicate"}, errText: "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(tt.args)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if want := tt.args[0] + ": not implemented yet"; err.Error() != want {
					t.Errorf("err = %q, want %q", err, want)
				}
			}
			if tt.errText != "" && !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("err = %q, want containing %q", err, tt.errText)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v1.2.3-rc.1"

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out.String()), "sb version v1.2.3-rc.1"; got != want {
		t.Errorf("--version = %q, want %q", got, want)
	}
}

func TestHelperHiddenFromHelp(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	if strings.Contains(help, "helper") {
		t.Error("helper should be hidden from help output")
	}
	for _, cmd := range []string{"daemon", "add", "rm", "ls", "open", "doctor", "setup", "uninstall"} {
		if !strings.Contains(help, cmd) {
			t.Errorf("help missing %q", cmd)
		}
	}
}
