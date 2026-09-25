package linux

import (
	"errors"
	"os/exec"
)

// AdminCommand returns a command that runs argv as root after polkit asks for
// an administrator's password in a dialog (pkexec), for apps without a
// terminal. pkexec shows its own prompt, so prompt is unused.
func (p *Platform) AdminCommand(argv []string, _ string) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, errors.New("admin command: empty argv")
	}
	path, err := exec.LookPath("pkexec")
	if err != nil {
		return nil, errors.New("pkexec not found; install polkit, or run the command in a terminal")
	}
	return exec.Command(path, argv...), nil //nolint:gosec // G204: argv is our own binary and flags
}
