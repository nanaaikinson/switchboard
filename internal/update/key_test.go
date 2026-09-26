package update

import (
	"os"
	"regexp"
	"testing"
)

// The install scripts verify SHA256SUMS with the same release key sb verifies
// updates with; a key rotated in one place only would break installs or
// updates.
func TestInstallScriptsPinReleaseKey(t *testing.T) {
	for path, re := range map[string]*regexp.Regexp{
		"../../install/install.sh":  regexp.MustCompile(`(?m)^MINISIGN_PUBKEY="([^"]*)"$`),
		"../../install/install.ps1": regexp.MustCompile(`(?m)^\$SbMinisignPubkey = '([^']*)'`),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := re.FindSubmatch(data)
		if m == nil {
			t.Fatalf("%s: no key slot", path)
		}
		if got := string(m[1]); got != ReleaseKey {
			t.Errorf("%s pins %q, ReleaseKey is %q", path, got, ReleaseKey)
		}
	}
	if _, err := ParsePublicKey(ReleaseKey); err != nil {
		t.Errorf("ReleaseKey: %v", err)
	}
}
