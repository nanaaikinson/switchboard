package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenHelperLog(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "sb-helper-123.log")
	if err := os.WriteFile(good, []byte("start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openHelperLog(good)
	if err != nil {
		t.Fatalf("setup's own log refused: %v", err)
	}
	_, err = f.WriteString("more\n")
	_ = f.Close()
	if b, _ := os.ReadFile(good); err != nil || string(b) != "start\nmore\n" {
		t.Errorf("log = %q, %v; want appended", b, err)
	}

	target := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	refuse := map[string]string{
		"wrong name": target,
		"relative":   "sb-helper-1.log",
		"missing":    filepath.Join(dir, "sb-helper-missing.log"),
	}
	if err := os.Link(target, filepath.Join(dir, "sb-helper-hard.log")); err == nil {
		refuse["hard link"] = filepath.Join(dir, "sb-helper-hard.log")
	}
	// Symbolic links need Developer Mode or an elevated runner.
	if err := os.Symlink(target, filepath.Join(dir, "sb-helper-sym.log")); err == nil {
		refuse["symlink"] = filepath.Join(dir, "sb-helper-sym.log")
	}
	if err := os.Mkdir(filepath.Join(dir, "sb-helper-dir.log"), 0o700); err == nil {
		refuse["directory"] = filepath.Join(dir, "sb-helper-dir.log")
	}
	for name, path := range refuse {
		if f, err := openHelperLog(path); err == nil {
			_ = f.Close()
			t.Errorf("%s: opened %s", name, path)
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("victim changed: %q", b)
	}
}
