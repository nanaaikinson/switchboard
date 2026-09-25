package darwin

import (
	"errors"
	"os/exec"
	"strings"
)

// AdminCommand returns a command that runs argv as root after macOS asks for
// an administrator's password in its own dialog, for apps without a
// terminal. prompt is shown in the dialog.
func (p *Platform) AdminCommand(argv []string, prompt string) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, errors.New("admin command: empty argv")
	}
	script := "do shell script " + appleScriptString(shellQuote(argv)) +
		" with prompt " + appleScriptString(prompt) + " with administrator privileges"
	return exec.Command("/usr/bin/osascript", "-e", script), nil //nolint:gosec // G204: argv is our own binary and flags, quoted twice below
}

// shellQuote joins argv for /bin/sh, single-quoting every word.
func shellQuote(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// appleScriptString is s as an AppleScript string literal.
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
