//go:build darwin || linux

package posix

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Files makes root's file changes. Every path is joined to Root, which is "/"
// except in tests. Writes into the user's home go only below directories that
// CheckUserDir accepts.
type Files struct {
	Root  string
	UID   int                                  // the user whose home is written to
	Chown func(f *os.File, uid, gid int) error // nil is (*os.File).Chown
}

// Path is path under Root.
func (f Files) Path(path string) string { return filepath.Join(f.Root, path) }

func (f Files) chown(file *os.File, uid, gid int) error {
	if f.Chown != nil {
		return f.Chown(file, uid, gid)
	}
	return file.Chown(uid, gid)
}

// UserGID is the primary group of uid.
func UserGID(uid int) (int, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return 0, fmt.Errorf("look up uid %d: %w", uid, err)
	}
	return strconv.Atoi(u.Gid)
}

// UserName is the login name of uid.
func UserName(uid int) (string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", fmt.Errorf("look up uid %d: %w", uid, err)
	}
	return u.Username, nil
}

// Remove deletes path; a missing file is not an error.
func (f Files) Remove(path string) error {
	if err := os.Remove(f.Path(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// RemoveUserFile removes a file in the user's home, but only when its parent
// is a real directory owned by the user, so root never deletes through a
// symlinked directory.
func (f Files) RemoveUserFile(path string) error {
	if _, err := os.Lstat(f.Path(filepath.Dir(path))); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := f.CheckUserDir(filepath.Dir(path)); err != nil {
		return err
	}
	return f.Remove(path)
}

// CheckUserDir requires dir to be a real directory (not a symlink) owned by
// the user. Root writes into the user's home only below such directories.
func (f Files) CheckUserDir(dir string) error {
	fi, err := os.Lstat(f.Path(dir))
	if err != nil {
		return fmt.Errorf("check %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s must be a directory, not a symlink or file", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != f.UID {
		return fmt.Errorf("%s is not owned by uid %d", dir, f.UID)
	}
	return nil
}

// MkdirOwned creates dir (one level) owned by the user if it is missing. The
// new directory is opened without following symlinks before it is chowned.
func (f Files) MkdirOwned(dir string, gid int) error {
	if _, err := os.Lstat(f.Path(dir)); err == nil {
		return nil
	}
	if err := os.Mkdir(f.Path(dir), 0o755); err != nil { //nolint:gosec // G301: conventional mode for config dirs in a home
		return fmt.Errorf("create %s: %w", dir, err)
	}
	d, err := os.OpenFile(f.Path(dir), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer d.Close()
	if err := f.chown(d, f.UID, gid); err != nil {
		return fmt.Errorf("chown %s: %w", dir, err)
	}
	return nil
}

// MkdirChainOwned creates each missing directory from home down to dir,
// owned by the user, checking every level. dir must be below home.
func (f Files) MkdirChainOwned(home, dir string, gid int) error {
	rel, err := filepath.Rel(home, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("%s is not below %s", dir, home)
	}
	if err := f.CheckUserDir(home); err != nil {
		return err
	}
	cur := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if err := f.MkdirOwned(cur, gid); err != nil {
			return err
		}
		if err := f.CheckUserDir(cur); err != nil {
			return err
		}
	}
	return nil
}

// WriteFile atomically writes data to path with the given mode and owner. The
// temp file is created with O_EXCL and renamed over path, so a symlink planted
// at path is replaced rather than followed.
func (f Files) WriteFile(path string, data []byte, mode os.FileMode, uid, gid int) error {
	full := f.Path(path)
	tmp, err := os.CreateTemp(filepath.Dir(full), "."+filepath.Base(full)+".sb-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := f.chown(tmp, uid, gid); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chown %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(name, full); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// CopyRootOwned installs a root-owned copy of the user's binary at dst, so a
// root service never runs a binary the user can replace.
func (f Files) CopyRootOwned(src, dst string) error {
	in, err := os.Open(f.Path(src))
	if err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	defer in.Close()
	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := os.MkdirAll(f.Path(filepath.Dir(dst)), 0o755); err != nil { //nolint:gosec // G301: system binary dir
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return f.WriteFile(dst, data, 0o755, 0, 0)
}
