package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxBinary bounds the sb binary read from an archive.
const maxBinary = 200 << 20

// Managed says that something else owns this sb, and how to update it.
type Managed struct {
	By     string // "Homebrew", "winget", "Scoop", "a Linux package", "the Switchboard app"
	Update string // what to run instead
}

// System is what DetectManaged may look at; the zero value uses the real one.
type System struct {
	ReadFile func(string) ([]byte, error)
	Run      func(name string, args ...string) error // nil error: the command succeeded
}

// DetectManaged reports whether the binary at exe (symlinks resolved) was
// installed by a package manager or the tray app, which must update it
// themselves: replacing their files would break them. It returns nil for a
// standalone install (install.sh, a release archive).
func DetectManaged(exe string, sys System) *Managed {
	if sys.ReadFile == nil {
		sys.ReadFile = os.ReadFile
	}
	p := strings.ToLower(strings.ReplaceAll(exe, `\`, "/")) // Windows paths too, whatever the OS
	switch {
	case strings.Contains(p, "/cellar/") || strings.HasPrefix(p, "/opt/homebrew/") || strings.HasPrefix(p, "/home/linuxbrew/.linuxbrew/"):
		return &Managed{"Homebrew", "brew upgrade switchboard"}
	case strings.Contains(p, "/microsoft/winget/") || strings.Contains(p, "/winget/packages/") || strings.Contains(p, "/windowsapps/"):
		return &Managed{"winget", "winget upgrade switchboard"}
	case strings.Contains(p, "/scoop/apps/"):
		return &Managed{"Scoop", "scoop update switchboard"}
	case strings.Contains(p, ".app/contents/macos/"):
		return &Managed{"the Switchboard app", "use Check for Updates… in the Switchboard menu"}
	}
	// Debian packages list their files; rpm can say who owns a path.
	if list, err := sys.ReadFile("/var/lib/dpkg/info/switchboard.list"); err == nil {
		for _, line := range strings.Split(string(list), "\n") {
			if strings.TrimSpace(line) == exe {
				return &Managed{"a Linux package (.deb)", "sudo apt install --only-upgrade switchboard, or install the new .deb"}
			}
		}
	}
	if sys.Run != nil && strings.HasPrefix(exe, "/usr/") && sys.Run("rpm", "-qf", "--quiet", exe) == nil {
		return &Managed{"a Linux package (.rpm)", "sudo dnf upgrade switchboard, or install the new .rpm"}
	}
	return nil
}

// OldPath is where Swap keeps the previous binary: sb.old (sb.old.exe on
// Windows, so it still runs).
func OldPath(exe string) string {
	ext := filepath.Ext(exe)
	if !strings.EqualFold(ext, ".exe") {
		ext = ""
	}
	return strings.TrimSuffix(exe, ext) + ".old" + ext
}

// Stage writes data next to exe as an executable temp file, so the swap is a
// rename on the same file system. The caller should check it runs.
func Stage(exe string, data []byte) (string, error) {
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(exe); err == nil {
		mode = fi.Mode().Perm() | 0o100
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".sb-new-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("can't write to %s: %w; update sb where you installed it, or re-run install.sh", filepath.Dir(exe), err)
		}
		return "", err
	}
	name := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(name, mode)); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	return name, nil
}

// Swap replaces exe with staged, keeping the old binary at OldPath(exe). If
// the second rename fails, exe is put back.
func Swap(exe, staged string) error {
	old := OldPath(exe)
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", old, err)
	}
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("keep the current binary as %s: %w", old, err)
	}
	if err := os.Rename(staged, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fmt.Errorf("install the new binary: %w (and restoring %s failed: %w)", err, exe, rerr)
		}
		return fmt.Errorf("install the new binary: %w; the old one is still in place", err)
	}
	return nil
}

// ErrNoOld means there is no previous binary to roll back to.
var ErrNoOld = errors.New("no previous version to roll back to")

// Rollback swaps exe with OldPath(exe). Running it again rolls forward.
func Rollback(exe string) error {
	old := OldPath(exe)
	if _, err := os.Stat(old); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w (%s doesn't exist; it's created by sb self-update)", ErrNoOld, old)
	}
	tmp := exe + ".rollback"
	_ = os.Remove(tmp)
	if err := os.Rename(exe, tmp); err != nil {
		return fmt.Errorf("move %s aside: %w", exe, err)
	}
	if err := os.Rename(old, exe); err != nil {
		if rerr := os.Rename(tmp, exe); rerr != nil {
			return fmt.Errorf("restore %s: %w (and putting the current one back failed: %w)", old, err, rerr)
		}
		return fmt.Errorf("restore %s: %w", old, err)
	}
	if err := os.Rename(tmp, old); err != nil {
		return fmt.Errorf("rolled back, but keeping the newer binary as %s failed: %w", old, err)
	}
	return nil
}

// Extract returns the sb binary from a release archive: a .tar.gz, or a .zip
// on Windows. The archive holds sb_<version>_<os>_<arch>/sb[.exe].
func Extract(archive []byte, windows bool) ([]byte, error) {
	name := "sb"
	if windows {
		name = "sb.exe"
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("open zip: %w", err)
		}
		for _, f := range zr.File {
			if pathBase(f.Name) == name && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return readLimited(rc)
			}
		}
		return nil, fmt.Errorf("no %s in the archive", name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("no %s in the archive", name)
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if h.Typeflag == tar.TypeReg && pathBase(h.Name) == name {
			return readLimited(tr)
		}
	}
}

func pathBase(name string) string { return name[strings.LastIndexByte(name, '/')+1:] }

func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBinary+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBinary {
		return nil, errors.New("sb in the archive is implausibly large")
	}
	return b, nil
}
