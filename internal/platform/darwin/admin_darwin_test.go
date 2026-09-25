package darwin

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAdminCommand(t *testing.T) {
	c, err := New(Options{}).AdminCommand([]string{"/Applications/Switch board.app/sb", "helper", "install", "--home", `/Users/o'brien "x"`}, `Switchboard needs "admin"`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != "/usr/bin/osascript" || len(c.Args) != 3 || c.Args[1] != "-e" {
		t.Fatalf("cmd %v", c.Args)
	}
	want := `do shell script "'/Applications/Switch board.app/sb' 'helper' 'install' '--home' '/Users/o'\\''brien \"x\"'"` +
		` with prompt "Switchboard needs \"admin\"" with administrator privileges`
	if c.Args[2] != want {
		t.Errorf("script\n got %s\nwant %s", c.Args[2], want)
	}
	if _, err := New(Options{}).AdminCommand(nil, "x"); err == nil {
		t.Error("empty argv accepted")
	}
}

// The quoting must round-trip through a real shell: the words come back
// exactly, and nothing is executed.
func TestShellQuoteRoundTrip(t *testing.T) {
	words := []string{"/bin/echo", "a b", "it's", `"q"`, "$(touch /tmp/sb-pwned)", "`id`", `back\slash`, "*"}
	out, err := exec.Command("/bin/sh", "-c", "printf '%s\\n' "+shellQuote(words[1:])).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); strings.Join(got, "|") != strings.Join(words[1:], "|") {
		t.Errorf("round trip: %q", got)
	}
}
