package windows

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPipeName(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	a := PipeName("S-1-5-21-1-2-3-1001", `C:\Users\me\AppData\Roaming\switchboard`, id)
	if !strings.HasPrefix(a, `\\.\pipe\switchboard-`) || len(a) != len(`\\.\pipe\switchboard-`)+16 {
		t.Errorf("name %q", a)
	}
	if a != PipeName("s-1-5-21-1-2-3-1001", `c:\users\me\appdata\roaming\switchboard`, id) {
		t.Error("case changes the name (SIDs and Windows paths are case-insensitive)")
	}
	for _, tc := range []struct{ why, sid, dir, id string }{
		{"another user", "S-1-5-21-1-2-3-1002", `C:\Users\me\AppData\Roaming\switchboard`, id},
		{"another config dir", "S-1-5-21-1-2-3-1001", `C:\tmp\sbtest`, id},
		// Without the random id, anyone who knows the SID and the default
		// config dir could create the pipe before the daemon does.
		{"another install", "S-1-5-21-1-2-3-1001", `C:\Users\me\AppData\Roaming\switchboard`, "fedcba9876543210fedcba9876543210"},
	} {
		if a == PipeName(tc.sid, tc.dir, tc.id) {
			t.Errorf("%s gets the same pipe", tc.why)
		}
	}
	if strings.Contains(a, "1001") || strings.Contains(a, id[:8]) {
		t.Error("the name reveals the SID or the id")
	}
}

func TestPipeID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "switchboard") // not created yet
	// The daemon and sb race to create it on first use: all get the same id.
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			id, err := PipeID(dir)
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		})
	}
	wg.Wait()
	for _, id := range ids {
		if len(id) != 32 || id != ids[0] {
			t.Fatalf("ids = %q, want one 32-digit id", ids)
		}
	}
	if again, err := PipeID(dir); err != nil || again != ids[0] {
		t.Errorf("second read = %q, %v", again, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("config dir has %d entries, want only %s", len(entries), PipeIDFile)
	}
	if fi, err := os.Stat(filepath.Join(dir, PipeIDFile)); err != nil || (fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Errorf("stat: %v, %v; want mode 0600", fi.Mode(), err)
	}
	if err := os.WriteFile(filepath.Join(dir, PipeIDFile), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PipeID(dir); err == nil || !strings.Contains(err.Error(), "delete it") {
		t.Errorf("corrupt file: %v", err)
	}
}

func TestPipeSDDL(t *testing.T) {
	if got := PipeSDDL("S-1-5-21-1-2-3-1001"); got != "D:P(A;;GA;;;S-1-5-21-1-2-3-1001)" {
		t.Errorf("SDDL %q", got)
	}
}
