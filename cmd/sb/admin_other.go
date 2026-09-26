//go:build !windows

package main

import (
	"os"
	"syscall"
)

func isAdmin() bool { return os.Geteuid() == 0 }

// openHelperLog opens the helper's log file. Only Windows' sb setup passes
// one (sudo keeps the terminal elsewhere).
func openHelperLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: the path sb setup made
}

// checkUserSID is only needed on Windows, where users are named by SID.
func checkUserSID(string, string) error { return nil }
