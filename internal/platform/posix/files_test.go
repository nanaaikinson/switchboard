//go:build darwin || linux

package posix

import (
	"os"
	"path/filepath"
	"testing"
)

// homeFixture makes <root>/home/me owned by the test user, and <root>/etc
// standing in for a root-owned system dir outside it.
func homeFixture(t *testing.T) (Files, string, string) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"home/me", "etc/systemd/system"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := Files{Root: root, UID: os.Getuid(), Chown: func(*os.File, int, int) error { return nil }}
	return f, "/home/me", filepath.Join(root, "etc/systemd/system")
}

func TestHomeWrites(t *testing.T) {
	f, home, etc := homeFixture(t)
	h, err := f.OpenHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	unitDir := "/home/me/.config/systemd/user"
	if err := h.MkdirAll(unitDir, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteFile(unitDir+"/switchboard.service", []byte("unit"), 0o644, 0); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(f.Path(unitDir + "/switchboard.service"))
	if err != nil || string(got) != "unit" {
		t.Fatalf("read back %q: %v", got, err)
	}
	if fi, _ := os.Stat(f.Path(unitDir + "/switchboard.service")); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(f.Path(unitDir))
	if len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
	if err := h.Remove(unitDir + "/switchboard.service"); err != nil {
		t.Fatal(err)
	}
	if err := h.Remove(unitDir + "/switchboard.service"); err != nil {
		t.Errorf("removing a missing file: %v", err)
	}
	if err := h.WriteFile("/etc/passwd", nil, 0o644, 0); err == nil {
		t.Error("wrote outside the home")
	}
	if entries, _ := os.ReadDir(etc); len(entries) != 0 {
		t.Errorf("wrote into the system dir: %v", entries)
	}
}

// A directory swapped for a symlink that leads out of the home, as a process
// running as the user could do between a check and the write, is never
// followed: root must not create files in a system dir.
func TestHomeRefusesSymlinksOut(t *testing.T) {
	for name, link := range map[string]string{
		"final dir":        ".config/systemd/user",
		"intermediate dir": ".config/systemd",
		"top dir":          ".config",
	} {
		t.Run(name, func(t *testing.T) {
			f, home, etc := homeFixture(t)
			parent := filepath.Dir(f.Path(filepath.Join(home, link)))
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(etc, f.Path(filepath.Join(home, link))); err != nil {
				t.Fatal(err)
			}
			h, err := f.OpenHome(home)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			unit := "/home/me/.config/systemd/user/switchboard.service"
			if err := h.MkdirAll(filepath.Dir(unit), 0); err == nil {
				t.Error("MkdirAll went through the symlink")
			}
			if err := h.WriteFile(unit, []byte("unit"), 0o644, 0); err == nil {
				t.Error("WriteFile went through the symlink")
			}
			if err := h.Remove(unit); err == nil {
				if _, serr := os.Lstat(f.Path(filepath.Join(home, link))); serr != nil {
					t.Error("Remove deleted the link")
				}
			}
			var found []string
			_ = filepath.Walk(filepath.Dir(filepath.Dir(etc)), func(p string, fi os.FileInfo, _ error) error {
				if fi != nil && !fi.IsDir() {
					found = append(found, p)
				}
				return nil
			})
			if len(found) != 0 {
				t.Errorf("files created outside the home: %v", found)
			}
		})
	}
}

func TestHomeRemoveDeletesLinkNotTarget(t *testing.T) {
	f, home, etc := homeFixture(t)
	target := filepath.Join(etc, "important")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.Path(home+"/sb.log")); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveUserFile(home, home+"/sb.log"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("target removed: %v", err)
	}
	if err := f.RemoveUserFile("/home/nobody", "/home/nobody/x"); err != nil {
		t.Errorf("missing home: %v", err)
	}
}

func TestOpenHomeChecksOwner(t *testing.T) {
	f, home, _ := homeFixture(t)
	f.UID++
	if _, err := f.OpenHome(home); err == nil {
		t.Error("opened a home the user doesn't own")
	}
	f, home, etc := homeFixture(t)
	if err := os.RemoveAll(f.Path(home)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(etc, f.Path(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.OpenHome(home); err == nil {
		t.Error("opened a symlinked home")
	}
}
