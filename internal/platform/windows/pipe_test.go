package windows

import (
	"strings"
	"testing"
)

func TestPipeName(t *testing.T) {
	a := PipeName("S-1-5-21-1-2-3-1001", `C:\Users\me\AppData\Roaming\switchboard`)
	if !strings.HasPrefix(a, `\\.\pipe\switchboard-`) || len(a) != len(`\\.\pipe\switchboard-`)+16 {
		t.Errorf("name %q", a)
	}
	if a != PipeName("s-1-5-21-1-2-3-1001", `c:\users\me\appdata\roaming\switchboard`) {
		t.Error("case changes the name (SIDs and Windows paths are case-insensitive)")
	}
	if a == PipeName("S-1-5-21-1-2-3-1002", `C:\Users\me\AppData\Roaming\switchboard`) {
		t.Error("another user gets the same pipe")
	}
	if a == PipeName("S-1-5-21-1-2-3-1001", `C:\tmp\sbtest`) {
		t.Error("another config dir gets the same pipe")
	}
	if strings.Contains(a, "1001") {
		t.Error("the name reveals the SID")
	}
}

func TestPipeSDDL(t *testing.T) {
	if got := PipeSDDL("S-1-5-21-1-2-3-1001"); got != "D:P(A;;GA;;;S-1-5-21-1-2-3-1001)" {
		t.Errorf("SDDL %q", got)
	}
}
