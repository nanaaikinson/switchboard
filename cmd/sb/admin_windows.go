package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// isAdmin reports whether this process runs elevated.
func isAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

// openHelperLog opens the log file that the unelevated 'sb setup' made for
// the elevated helper. The path comes from the command line, and with
// over-the-shoulder UAC the helper runs as a different (admin) account, so it
// refuses anything but a plain, singly-linked file with the name setup uses,
// reached without symbolic links or junctions. Otherwise a standard user could
// make an administrator append to a file of their choice.
func openHelperLog(path string) (*os.File, error) {
	if ok, _ := filepath.Match("sb-helper-*.log", filepath.Base(path)); !ok || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("log %q is not an sb-helper-*.log file", path)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// FILE_APPEND_DATA without FILE_WRITE_DATA: writes can only append.
	// FILE_FLAG_OPEN_REPARSE_POINT opens a symbolic link itself, not its target.
	h, err := windows.CreateFile(p, windows.FILE_APPEND_DATA, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	if err := checkHelperLog(h, path); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func checkHelperLog(h windows.Handle, path string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fmt.Errorf("log: %w", err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return errors.New("log is a link or a directory")
	}
	if info.NumberOfLinks != 1 {
		return errors.New("log has more than one hard link")
	}
	// A junction in a parent directory would lead elsewhere: the file must be
	// where the path says. Short (8.3) names in the path are expanded first.
	final, err := finalPath(h)
	if err != nil {
		return err
	}
	want := path
	if long, err := longPath(path); err == nil {
		want = long
	}
	if !strings.EqualFold(filepath.Clean(final), filepath.Clean(want)) {
		return fmt.Errorf("log %s resolves to %s", path, final)
	}
	return nil
}

func finalPath(h windows.Handle) (string, error) {
	var buf [windows.MAX_LONG_PATH]uint16
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], windows.MAX_LONG_PATH, 0)
	if err != nil {
		return "", fmt.Errorf("log path: %w", err)
	}
	if int(n) >= len(buf) {
		return "", errors.New("log path too long")
	}
	return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`), nil
}

func longPath(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var buf [windows.MAX_LONG_PATH]uint16
	n, err := windows.GetLongPathName(p, &buf[0], windows.MAX_LONG_PATH)
	if err != nil {
		return "", err
	}
	if int(n) >= len(buf) {
		return "", errors.New("path too long")
	}
	return windows.UTF16ToString(buf[:n]), nil
}
