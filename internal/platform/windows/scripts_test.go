package windows

import (
	"errors"
	"strings"
	"testing"
)

// parseCommandLine is CommandLineToArgvW's documented algorithm for
// arguments after the program name, so quoteArg can be round-tripped.
func parseCommandLine(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\':
			n := 0
			for i < len(s) && s[i] == '\\' {
				n++
				i++
			}
			if i < len(s) && s[i] == '"' {
				cur.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					cur.WriteByte('"')
					i++
				}
			} else {
				cur.WriteString(strings.Repeat(`\`, n))
			}
			have = true
			continue
		case c == '"':
			inQuote = !inQuote
			have = true
		case (c == ' ' || c == '\t') && !inQuote:
			if have {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
		i++
	}
	if have {
		args = append(args, cur.String())
	}
	return args
}

func TestQuoteArgRoundTrips(t *testing.T) {
	words := []string{"helper", "install", "--home", `C:\Users\Jane Doe`, `C:\path with\trailing\`, `say "hi"`,
		`back\\"slash`, "", `\\server\share\`, "tab\there", `C:\Program Files\Switchboard\sb.exe`, "plain"}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quoteArg(w)
	}
	got := parseCommandLine(strings.Join(quoted, " "))
	if strings.Join(got, "|") != strings.Join(words, "|") {
		t.Errorf("round trip\n got %q\nwant %q", got, words)
	}
	if quoteArg("plain") != "plain" || quoteArg(`C:\a\b`) != `C:\a\b` {
		t.Error("simple arguments should not be quoted")
	}
}

func TestPSQuote(t *testing.T) {
	if got := psQuote(`O'Brien $(rm -r C:\) "x"`); got != `'O''Brien $(rm -r C:\) "x"'` {
		t.Errorf("psQuote = %s", got)
	}
}

func TestAdminScript(t *testing.T) {
	s := adminScript([]string{`C:\Users\Jane Doe\bin\sb.exe`, "helper", "install", "--home", `C:\Users\Jane Doe`})
	for _, want := range []string{
		`Start-Process -FilePath 'C:\Users\Jane Doe\bin\sb.exe'`,
		`-ArgumentList 'helper install --home "C:\Users\Jane Doe"'`,
		"-Verb RunAs -Wait -PassThru", "exit $p.ExitCode", "$ErrorActionPreference = 'Stop'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
}

func TestNRPTScripts(t *testing.T) {
	add := nrptScript("add", "test")
	for _, want := range []string{"$_.Namespace -contains '.test'", "Add-DnsClientNrptRule -Namespace '.test' -NameServers '127.0.0.1' -Comment '" + Marker + "'",
		"Write-Output 'FOREIGN'; exit 3", "Clear-DnsClientCache"} {
		if !strings.Contains(add, want) {
			t.Errorf("add script missing %q:\n%s", want, add)
		}
	}
	if rm := nrptScript("remove", "test"); !strings.Contains(rm, "$ours | ForEach-Object { Remove-DnsClientNrptRule") || strings.Contains(rm, "$rules | ForEach-Object { Remove") {
		t.Errorf("remove must only remove Switchboard's rules:\n%s", rm)
	}
}

func TestParseNRPT(t *testing.T) {
	for in, want := range map[string]int{
		"":     0,
		"null": 0,
		`{"Namespace":[".test"],"NameServers":["127.0.0.1"],"Comment":"` + Marker + `"}`:                                                      1,
		`[{"Namespace":[".test"],"NameServers":["10.0.0.1"],"Comment":""},{"Namespace":[".test"],"NameServers":["127.0.0.1"],"Comment":"x"}]`: 2,
	} {
		rules, err := parseNRPT([]byte(in))
		if err != nil || len(rules) != want {
			t.Errorf("parseNRPT(%.30q) = %v, %v", in, rules, err)
		}
	}
	if _, err := parseNRPT([]byte("Get-DnsClientNrptRule : not recognized")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestTaskScripts(t *testing.T) {
	s := taskScript(`CONTOSO\jane`, "S-1-5-21-1-2-3-1001", `C:\Users\Jane Doe\AppData\Local\Programs\switchboard\sb.exe`)
	for _, want := range []string{
		`$user = 'CONTOSO\jane'`,
		`-Argument '--headless "C:\Users\Jane Doe\AppData\Local\Programs\switchboard\sb.exe" daemon'`,
		"-AtLogOn -User $user", "-LogonType Interactive -RunLevel Limited",
		`-TaskPath '\Switchboard\' -TaskName 'Daemon-S-1-5-21-1-2-3-1001'`, "Start-ScheduledTask",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("task script missing %q:\n%s", want, s)
		}
	}
	u := untaskScript("S-1-5-21-1-2-3-1001")
	if !strings.Contains(u, "$t.Description -ne '"+Marker+"'") || !strings.Contains(u, "Unregister-ScheduledTask") {
		t.Errorf("untask script:\n%s", u)
	}
	r := restartScript("S-1-5-21-1-2-3-1001")
	if !strings.Contains(r, "Write-Output 'GONE'") || !strings.Contains(r, "Stop-ScheduledTask -TaskPath '\\Switchboard\\' -TaskName 'Daemon-S-1-5-21-1-2-3-1001'") {
		t.Errorf("restart script:\n%s", r)
	}
}

func TestListenerScript(t *testing.T) {
	s := listenerScript(443)
	if !strings.Contains(s, "-State Listen -LocalPort 443 ") || !strings.Contains(s, "`t") {
		t.Errorf("listener script:\n%s", s)
	}
}

const serviceState = `
Snapshot of HTTP service state (Request Queue View):
-----------------------------------------------------

Server session ID: FF00000020000001
    Version: 2.0
    State: Active

Request queue name: DefaultAppPool
    Version: 2.0
    State: Active
    Request queue 503 verbosity level: Basic
    Max requests: 1000
    Number of active processes attached: 1
    Process IDs:
        3248
    URL groups:
    URL group ID: FE00000040000001
        State: Active
        Request queue name: DefaultAppPool
        Properties:
            Max bandwidth: inherited
        Number of registered URLs: 1
        Registered URLs:
            HTTP://*:80/

Request queue name: Request queue is unnamed.
    Version: 2.0
    State: Active
    Number of active processes attached: 1
    Process IDs:
        4120
    URL groups:
    URL group ID: FD00000040000002
        Number of registered URLs: 2
        Registered URLs:
            HTTP://+:5357/
            HTTPS://+:443/SSTP_SSL/
`

func TestParseServiceState(t *testing.T) {
	qs := parseServiceState(strings.ReplaceAll(serviceState, "\n", "\r\n"))
	if len(qs) < 2 {
		t.Fatalf("queues %+v", qs)
	}
	on80 := QueuesOnPort(qs, 80)
	if len(on80) != 1 || on80[0].Name != "DefaultAppPool" || len(on80[0].PIDs) != 1 || on80[0].PIDs[0] != 3248 {
		t.Errorf("port 80: %+v", on80)
	}
	on443 := QueuesOnPort(qs, 443)
	if len(on443) != 1 || on443[0].PIDs[0] != 4120 || on443[0].URLs[1] != "HTTPS://+:443/SSTP_SSL/" {
		t.Errorf("port 443: %+v", on443)
	}
	if got := QueuesOnPort(qs, 8080); len(got) != 0 {
		t.Errorf("port 8080: %+v", got)
	}
}

func TestDescribeHTTPSys(t *testing.T) {
	qs := parseServiceState(serviceState)
	got := describeHTTPSys(QueuesOnPort(qs, 80), 80)
	// sb doctor keys its IIS advice off the "http.sys" prefix.
	if !strings.HasPrefix(got, "http.sys: ") || !strings.Contains(got, `"DefaultAppPool"`) || !strings.Contains(got, "pid 3248") {
		t.Errorf("port 80: %q", got)
	}
	if got := describeHTTPSys(nil, 443); !strings.HasPrefix(got, "http.sys (") || !strings.Contains(got, "443") {
		t.Errorf("no queue: %q", got)
	}
}

func TestValidTLD(t *testing.T) {
	for tld, want := range map[string]bool{
		"test": true, "dev-box": true, "x1": true,
		"": false, "Test": false, "-x": false, "x-": false, "a.b": false,
		"te'st": false, strings.Repeat("a", 64): false,
	} {
		if got := validTLD(tld); got != want {
			t.Errorf("validTLD(%q) = %v", tld, got)
		}
	}
}

func TestWithFix(t *testing.T) {
	base := errors.New("boom")
	err := withFix("Run 'sb setup'.", "check: %w", base)
	var f interface{ Fix() string }
	if !errors.As(err, &f) || f.Fix() != "Run 'sb setup'." || !errors.Is(err, base) || err.Error() != "check: boom" {
		t.Errorf("withFix: %v", err)
	}
}
