//go:build darwin || linux

package posix

import (
	"crypto/rand"
	"encoding/hex"
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

// CheckUserDir requires dir to be a real directory (not a symlink) owned by
// the user. Root writes into the user's home only through OpenHome.
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

// Home is root's handle on the user's home directory. Every change goes
// through an os.Root opened on it, so no path below home can lead outside it,
// even through a directory the user swaps for a symlink after a check, and
// every directory written into is checked through the handle it was opened
// with to be a real directory owned by the user.
type Home struct {
	f    Files
	dir  string // the home path, as given
	root *os.Root
}

// OpenHome opens the user's home, which must be a real directory they own.
func (f Files) OpenHome(home string) (*Home, error) {
	if err := f.CheckUserDir(home); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(f.Path(home))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", home, err)
	}
	h := &Home{f: f, dir: home, root: root}
	if err := h.checkOwned(".", home); err != nil {
		_ = root.Close()
		return nil, err
	}
	return h, nil
}

// Close releases the handle.
func (h *Home) Close() error { return h.root.Close() }

// rel is path relative to the home, which it must be below.
func (h *Home) rel(path string) (string, error) {
	rel, err := filepath.Rel(h.dir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s is not below %s", path, h.dir)
	}
	return rel, nil
}

// checkOwned opens rel without following a final symlink and checks, on the
// open handle, that it is a directory the user owns.
func (h *Home) checkOwned(rel, path string) error {
	d, err := h.root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("%s must be a directory you own, not a symlink or file: %w", path, err)
	}
	defer d.Close()
	fi, err := d.Stat()
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != h.f.UID {
		return fmt.Errorf("%s is not owned by uid %d", path, h.f.UID)
	}
	return nil
}

// CheckDir checks that dir, below the home, is a real directory the user owns.
func (h *Home) CheckDir(dir string) error {
	rel, err := h.rel(dir)
	if err != nil {
		return err
	}
	return h.checkOwned(rel, dir)
}

// MkdirAll creates each missing directory from the home down to dir, owned
// by the user, and checks every level.
func (h *Home) MkdirAll(dir string, gid int) error {
	rel, err := h.rel(dir)
	if err != nil {
		return err
	}
	cur := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		path := filepath.Join(h.dir, cur)
		if err := h.root.Mkdir(cur, 0o755); err == nil {
			d, err := h.root.OpenFile(cur, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
			if err != nil {
				return fmt.Errorf("open %s: %w", path, err)
			}
			err = h.f.chown(d, h.f.UID, gid)
			_ = d.Close()
			if err != nil {
				return fmt.Errorf("chown %s: %w", path, err)
			}
		} else if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", path, err)
		}
		if err := h.checkOwned(cur, path); err != nil {
			return err
		}
	}
	return nil
}

// WriteFile atomically writes data to path, below the home, owned by the
// user. Its directory must be a real directory the user owns.
func (h *Home) WriteFile(path string, data []byte, mode os.FileMode, gid int) error {
	rel, err := h.rel(path)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := h.checkOwned(dir, filepath.Dir(path)); err != nil {
			return err
		}
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmpRel := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".sb-"+hex.EncodeToString(rnd[:]))
	tmp, err := h.root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() { _ = h.root.Remove(tmpRel) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := h.f.chown(tmp, h.f.UID, gid); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chown %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := h.root.Rename(tmpRel, rel); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Remove deletes path, below the home; a missing file is not an error. A
// symlink is removed itself, never its target.
func (h *Home) Remove(path string) error {
	rel, err := h.rel(path)
	if err != nil {
		return err
	}
	if err := h.root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// RemoveUserFile removes path from the user's home, if the home is there.
func (f Files) RemoveUserFile(home, path string) error {
	if _, err := os.Lstat(f.Path(home)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	h, err := f.OpenHome(home)
	if err != nil {
		return err
	}
	defer h.Close()
	return h.Remove(path)
}

// WriteFile atomically writes data to path with the given mode and owner. The
// temp file is created with O_EXCL and renamed over path, so a symlink planted
// at path is replaced rather than followed. It is for root-owned directories;
// use Home for the user's.
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
