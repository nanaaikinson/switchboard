package windows

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Marker is the description Switchboard puts on what it creates (the NRPT
// rule's comment, the Scheduled Task's description), so uninstall only
// removes its own.
const Marker = "Managed by Switchboard; removed by sb uninstall"

// TaskPath is the Task Scheduler folder for Switchboard's tasks.
const TaskPath = `\Switchboard\`

// TaskName is the per-user logon task that runs the daemon.
func TaskName(sid string) string { return "Daemon-" + sid }

// psQuote is s as a PowerShell single-quoted string literal, in which only
// the quote itself is special.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Scripts run with $ErrorActionPreference = 'Stop', so a failing cmdlet
// ends the script with a non-zero exit code.
const psPrelude = "$ErrorActionPreference = 'Stop'\n$ProgressPreference = 'SilentlyContinue'\n"

// nrptScript adds, removes or reports Switchboard's NRPT rule for tld.
// "add" exits 3 with FOREIGN if another rule already covers the namespace.
func nrptScript(action, tld string) string {
	ns := psQuote("." + tld)
	find := `$rules = @(Get-DnsClientNrptRule | Where-Object { $_.Namespace -contains ` + ns + ` })
$ours = @($rules | Where-Object { $_.Comment -eq ` + psQuote(Marker) + ` })
`
	switch action {
	case "add":
		return psPrelude + find + `if ($rules.Count -gt $ours.Count) { Write-Output 'FOREIGN'; exit 3 }
$ours | ForEach-Object { Remove-DnsClientNrptRule -Name $_.Name -Force }
Add-DnsClientNrptRule -Namespace ` + ns + ` -NameServers '127.0.0.1' -Comment ` + psQuote(Marker) + ` | Out-Null
Clear-DnsClientCache
`
	case "remove":
		return psPrelude + find + `$ours | ForEach-Object { Remove-DnsClientNrptRule -Name $_.Name -Force }
if ($ours.Count -gt 0) { Clear-DnsClientCache }
if ($rules.Count -gt $ours.Count) { Write-Output 'FOREIGN' }
`
	default: // "show"
		return psPrelude + find + `ConvertTo-Json -Compress -InputObject @($rules | ForEach-Object {
  [pscustomobject]@{ Namespace = @($_.Namespace); NameServers = @($_.NameServers); Comment = [string]$_.Comment }
})
`
	}
}

// NRPTRule is one rule as nrptScript("show") reports it.
type NRPTRule struct {
	Namespace   []string
	NameServers []string
	Comment     string
}

// parseNRPT reads nrptScript("show") output: a JSON array, which PowerShell
// 5.1 prints as a single object or nothing when there are fewer than two.
func parseNRPT(out []byte) ([]NRPTRule, error) {
	s := strings.TrimSpace(string(out))
	if s == "" || s == "null" {
		return nil, nil
	}
	if strings.HasPrefix(s, "{") {
		s = "[" + s + "]"
	}
	var rules []NRPTRule
	if err := json.Unmarshal([]byte(s), &rules); err != nil {
		return nil, fmt.Errorf("read NRPT rules: %w", err)
	}
	return rules, nil
}

// taskScript registers (and starts) the logon task that runs the daemon as
// user, hidden: conhost --headless keeps a console app's window off screen.
func taskScript(user, sid, sbPath string) string {
	name, path := psQuote(TaskName(sid)), psQuote(TaskPath)
	arg := psQuote(`--headless "` + sbPath + `" daemon`)
	return psPrelude + `$user = ` + psQuote(user) + `
$a = New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\conhost.exe" -Argument ` + arg + `
$t = New-ScheduledTaskTrigger -AtLogOn -User $user
$p = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$s = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable ` +
		`-ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskPath ` + path + ` -TaskName ` + name + ` -Action $a -Trigger $t -Principal $p -Settings $s ` +
		`-Description ` + psQuote(Marker) + ` -Force | Out-Null
Start-ScheduledTask -TaskPath ` + path + ` -TaskName ` + name + `
`
}

// untaskScript stops and removes the task if it's Switchboard's, then the
// \Switchboard\ folder if it's empty. It prints GONE if there was none.
func untaskScript(sid string) string {
	name, path := psQuote(TaskName(sid)), psQuote(TaskPath)
	return psPrelude + `$t = Get-ScheduledTask -TaskPath ` + path + ` -TaskName ` + name + ` -ErrorAction SilentlyContinue
if (-not $t) { Write-Output 'GONE' } elseif ($t.Description -ne ` + psQuote(Marker) + `) { Write-Output 'FOREIGN'; exit 3 } else {
  Stop-ScheduledTask -TaskPath ` + path + ` -TaskName ` + name + `
  Unregister-ScheduledTask -TaskPath ` + path + ` -TaskName ` + name + ` -Confirm:$false
}
try {
  $svc = New-Object -ComObject Schedule.Service; $svc.Connect()
  if ($svc.GetFolder('\Switchboard').GetTasks(1).Count -eq 0) { $svc.GetFolder('\').DeleteFolder('Switchboard', 0) }
} catch { }
`
}

// restartScript restarts the user's own daemon task; GONE if there is none.
func restartScript(sid string) string {
	name, path := psQuote(TaskName(sid)), psQuote(TaskPath)
	return psPrelude + `$t = Get-ScheduledTask -TaskPath ` + path + ` -TaskName ` + name + ` -ErrorAction SilentlyContinue
if (-not $t) { Write-Output 'GONE'; exit 0 }
Stop-ScheduledTask -TaskPath ` + path + ` -TaskName ` + name + `
Start-Sleep -Milliseconds 500
Start-ScheduledTask -TaskPath ` + path + ` -TaskName ` + name + `
`
}

// listenerScript prints "<pid>\t<process name>" for the process listening
// on TCP port, or nothing.
func listenerScript(port int) string {
	return psPrelude + `$c = Get-NetTCPConnection -State Listen -LocalPort ` + strconv.Itoa(port) + ` -ErrorAction SilentlyContinue | Select-Object -First 1
if ($c) { $n = (Get-Process -Id $c.OwningProcess -ErrorAction SilentlyContinue).ProcessName; Write-Output "$($c.OwningProcess)` + "`t" + `$n" }
`
}

// adminScript runs argv elevated through UAC and exits with its exit code.
// Arguments are quoted for CommandLineToArgvW by quoteArg.
func adminScript(argv []string) string {
	args := make([]string, len(argv)-1)
	for i, a := range argv[1:] {
		args[i] = quoteArg(a)
	}
	return psPrelude + `$p = Start-Process -FilePath ` + psQuote(argv[0]) + ` -ArgumentList ` + psQuote(strings.Join(args, " ")) +
		` -Verb RunAs -Wait -PassThru -WindowStyle Hidden
exit $p.ExitCode
`
}

// quoteArg quotes one argument the way CommandLineToArgvW parses it back
// (like syscall.EscapeArg, which only exists on Windows).
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, 2*slashes+1))
			slashes = 0
			b.WriteByte('"')
			continue
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
			slashes = 0
			b.WriteByte(c)
			continue
		}
	}
	b.WriteString(strings.Repeat(`\`, 2*slashes)) // before the closing quote
	b.WriteByte('"')
	return b.String()
}

// HTTPSysQueue is one http.sys request queue from `netsh http show
// servicestate view=requestq`.
type HTTPSysQueue struct {
	Name string
	PIDs []int
	URLs []string
}

var urlPort = regexp.MustCompile(`(?i)^https?://[^/]*:(\d+)(/|$)`)

// parseServiceState reads `netsh http show servicestate view=requestq`.
func parseServiceState(out string) []HTTPSysQueue {
	var qs []HTTPSysQueue
	var cur *HTTPSysQueue
	section := ""
	for _, raw := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(raw, "Request queue name:"): // unindented; URL groups repeat it indented
			qs = append(qs, HTTPSysQueue{Name: strings.TrimSpace(strings.TrimPrefix(line, "Request queue name:"))})
			cur, section = &qs[len(qs)-1], ""
		case cur == nil:
		case strings.HasPrefix(line, "Process IDs:"):
			section = "pids"
		case strings.HasPrefix(line, "Registered URLs:"):
			section = "urls"
		case section == "pids" && isDigits(line):
			n, _ := strconv.Atoi(line)
			cur.PIDs = append(cur.PIDs, n)
		case section == "urls" && urlPort.MatchString(line):
			cur.URLs = append(cur.URLs, line)
		case strings.Contains(line, ":"):
			section = "" // another field ends the list
		}
	}
	return qs
}

// QueuesOnPort returns the queues with a URL registered on port.
func QueuesOnPort(qs []HTTPSysQueue, port int) []HTTPSysQueue {
	var out []HTTPSysQueue
	for _, q := range qs {
		for _, u := range q.URLs {
			if m := urlPort.FindStringSubmatch(u); m != nil && m[1] == strconv.Itoa(port) {
				out = append(out, q)
				break
			}
		}
	}
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// fixError is a diagnostic failure that knows its one-line fix; sb doctor
// finds it with errors.As on interface{ Fix() string }.
type fixError struct {
	err error
	fix string
}

func (e *fixError) Error() string { return e.err.Error() }
func (e *fixError) Unwrap() error { return e.err }
func (e *fixError) Fix() string   { return e.fix }

func withFix(fix, format string, args ...any) error {
	return &fixError{err: fmt.Errorf(format, args...), fix: fix}
}

// describeHTTPSys names what holds port through http.sys.
func describeHTTPSys(qs []HTTPSysQueue, port int) string {
	if len(qs) == 0 {
		return fmt.Sprintf("http.sys (a URL reservation on port %d; see 'netsh http show servicestate')", port)
	}
	parts := make([]string, len(qs))
	for i, q := range qs {
		pids := make([]string, len(q.PIDs))
		for j, pid := range q.PIDs {
			pids[j] = strconv.Itoa(pid)
		}
		s := fmt.Sprintf("request queue %q (%s)", q.Name, strings.Join(q.URLs, ", "))
		if len(pids) > 0 {
			s += " of pid " + strings.Join(pids, ", ")
		}
		parts[i] = s
	}
	return "http.sys: " + strings.Join(parts, "; ")
}

// validTLD reports whether tld is a single lowercase LDH label.
func validTLD(tld string) bool {
	if tld == "" || len(tld) > 63 || tld[0] == '-' || tld[len(tld)-1] == '-' {
		return false
	}
	for _, c := range tld {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
