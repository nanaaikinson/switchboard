# Windows setup and uninstall

`sb setup` on Windows makes three system changes after a single UAC prompt, and
`sb uninstall` reverses them after another. Unlike macOS and Linux there is no
privileged helper service: Windows lets any user bind ports below 1024, so the daemon
binds 53, 80 and 443 itself, as you.

## Requirements

- Windows 10 1809 or later, or Windows 11, on x64 or Arm64. 1809 is the first build with
  `conhost.exe --headless`, which keeps the daemon's window hidden.
- Windows PowerShell 5.1, which ships with Windows. `sb` uses it for the NRPT and
  Scheduled Task cmdlets (`DnsClient`, `ScheduledTasks`).
- An administrator account for the UAC prompt. You can run `sb setup` as a standard
  user and type an administrator's password (over-the-shoulder elevation): the daemon
  still runs as you.
- Ports 53, 80 and 443 free on 127.0.0.1. `sb doctor` names what holds them, including
  IIS and other `http.sys` users (see [Port conflicts](#port-conflicts)).

Install `sb` with [install.ps1](install.md#windows).

## What `sb setup` changes

| # | Change | Where | Removed by `sb uninstall` |
| --- | --- | --- | --- |
| 1 | An NRPT rule (Name Resolution Policy Table): `.test` lookups go to `127.0.0.1`. Its comment is `Managed by Switchboard; removed by sb uninstall` | `HKLM\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig` | Yes, only the rule with that comment |
| 2 | The Scheduled Task `\Switchboard\Daemon-<your SID>`: at your logon, runs `conhost.exe --headless "<sb.exe>" daemon` as you, not elevated (`RunLevel Limited`), restarts it on failure, no time limit. Started now too | Task Scheduler | Yes, stopped first, and only if its description is the marker above. The empty `\Switchboard` folder goes too |
| 3 | Switchboard's local CA (name-constrained to your TLDs) | `LocalMachine\Root` | Yes |

Nothing else: no service, no files outside your config dir
(`%APPDATA%\switchboard`), no `hosts` entries, no firewall rules (everything is on
loopback). Your routes and the CA files are kept by `sb uninstall`; delete
`%APPDATA%\switchboard` to remove them too.

Why each choice:

- **NRPT rather than a DNS server setting.** An NRPT rule applies to one namespace
  only, so the rest of your DNS is untouched, and it covers every adapter, VPNs
  included. NRPT names a server by IP address only, with no port, so the daemon's DNS
  server is on `127.0.0.1:53` on Windows (elsewhere `127.0.0.1:535`, bound by the root helper).
- **Known limit: the ports are first come, first served.** Windows has no privileged
  ports, so on a machine shared with other accounts, another user's process can bind
  `127.0.0.1:53`, `:80` or `:443` while your daemon is stopped, and see or answer
  traffic meant for Switchboard. It can't serve trusted HTTPS for your names, since it
  doesn't have your CA key. `sb doctor` names the process holding each port and fails
  if it runs as another account. On macOS and Linux the root helper binds these ports,
  so this can't happen there.
- **A Scheduled Task rather than a service.** Services run as SYSTEM or a service
  account; the daemon must run as you, in your logon session, to use your config dir
  and your control pipe.
- **`LocalMachine\Root` rather than truststore.** smallstep/truststore writes
  `CurrentUser\Root`, which asks for confirmation in a dialog on every add. The
  machine store is trusted by Edge, Chrome, Firefox (`security.enterprise_roots.enabled`,
  on by default) and Go programs, for every user.

## How elevation works

`sb setup` prints the plan, asks for confirmation, then runs itself elevated through
PowerShell's `Start-Process -Verb RunAs -Wait`: one UAC prompt. The elevated process is
`sb helper install --user DOMAIN\you --sid <your SID> --sb-path <sb.exe> ...` and does
only the three steps above. The unelevated `sb setup` passes your account name and SID,
and the helper checks that they match, so with over-the-shoulder elevation the task is
still registered for you, not the administrator.

An elevated process can't write to your console, so it writes its output to a
temporary `sb-helper-*.log` that `sb setup` created, which `sb setup` prints and
deletes afterwards. The helper refuses a log path that isn't a plain file with that
name, that has another hard link, or that is reached through a symbolic link or a
junction, so a standard user can't make an administrator's process append to a file of
their choice. If it refuses, it carries on with no log.

`sb trust`, `sb untrust` and `sb uninstall` work the same way.

## The control pipe

On Windows the CLI and the daemon talk over a named pipe,
`\\.\pipe\switchboard-<id>`, instead of a Unix socket. See [api.md](api.md). Only your
account can open it, and the CLI refuses to talk to a pipe served by another account.
Part of the name is random, kept in `%APPDATA%\switchboard\pipe-id`, so another account
can't guess it and take it first. If one does anyway, the daemon won't start and
`sb doctor` names that account; delete `pipe-id` and start the daemon again to move to
a new name.

## Port conflicts

`sb doctor` checks 127.0.0.1:53, :80 and :443. If a normal program holds a port, it
names it, e.g. `nginx (pid 1234)`. If the owner is PID 4 (System), the port belongs to
**http.sys**, the kernel HTTP server that IIS, WinRM, SSTP VPN, Windows Admin Center,
some Skype and SQL Server Reporting Services versions use. `sb doctor` then reads
`netsh http show servicestate view=requestq` and names the request queue and process:

```
[FAIL] port 80: held by http.sys: request queue "DefaultAppPool" (HTTP://*:80/) of pid 3248
       fix: Something registered port 80 with Windows' http.sys (see 'netsh http show servicestate'). Stop it (IIS: 'iisreset /stop' or remove the site's binding; other apps: their settings), then run 'sb setup' again.
```

Port 53 is sometimes held by Internet Connection Sharing (the `SharedAccess` service,
used by Mobile hotspot), a local DNS proxy (Acrylic, dnscrypt-proxy, Pi-hole for
Windows), or Docker Desktop and WSL options. Stop it, or choose which one runs.

## Diagnosing

These only read state:

```powershell
sb doctor
Get-DnsClientNrptRule | Where-Object Namespace -Contains '.test'
Resolve-DnsName anything.test
Get-ScheduledTask -TaskPath '\Switchboard\' | Format-List TaskName, State, Description
Get-ChildItem Cert:\LocalMachine\Root | Where-Object Subject -Like '*Switchboard*'
Get-NetTCPConnection -State Listen -LocalPort 53, 80, 443
netsh http show servicestate view=requestq
```

`nslookup` ignores NRPT and asks your normal DNS server, so it says `.test` names don't
exist even when they work. Use `Resolve-DnsName` or `ping`.

## Not yet on Windows

- The tray app ([tray.md](tray.md)) builds for Windows but its setup wizard and
  **Install Command-Line Tool…** are macOS-only.
- mDNS (`.local`) mode.

## Manual test checklist (Windows VM only)

Never run these on your main machine. Use a Windows 11 VM (Hyper-V, Parallels, UTM,
VirtualBox) or Windows Sandbox for the parts that don't need a logoff. Take a snapshot
first. Run everything from a normal (not elevated) PowerShell in the VM's desktop
session, not over SSH, so that UAC and logon tasks work as for a user.

Keep a second VM snapshot, or a second VM, with IIS installed for steps 24 to 27.

**Prepare**
1. On the host: `GOOS=windows GOARCH=amd64 go build -o sb.exe ./cmd/sb` (or `arm64`
   for an Arm VM). Copy it into the VM at `C:\Users\<you>\bin\sb.exe` and add that
   folder to your user `PATH` (or use install.ps1, steps 30 to 34).
2. Record a baseline:
   ```powershell
   Get-DnsClientNrptRule; Get-ScheduledTask -TaskPath '\Switchboard\' -ErrorAction SilentlyContinue; Get-ChildItem Cert:\LocalMachine\Root | Where-Object Subject -Like '*Switchboard*'; Get-NetTCPConnection -State Listen -LocalPort 53,80,443 -ErrorAction SilentlyContinue
   ```
   Expect: no `.test` rule, no task, no certificate, nothing listening.
3. `sb doctor` before setup: FAIL lines for the daemon, DNS and trust, each with a fix
   (mostly "run 'sb setup'"). The helper line is SKIP ("Windows needs no helper").

**Refusals**
4. `sb helper install` in the normal shell: "must run as administrator". Nothing
   changes.
5. `sb setup`, answer `n`: it lists 3 changes, says nothing was changed, and step 2's
   baseline is unchanged. No UAC prompt appeared.
6. `sb setup`, answer `y`, then **No** in the UAC prompt: a clear error that setup
   didn't run, and the baseline is unchanged.

**Install**
7. `sb setup`, answer `y`, then **Yes** in the UAC prompt. The helper's log is printed
   (the three steps), then "Setup complete". No console window flashes up and stays.
8. `Get-DnsClientNrptRule`: one rule, Namespace `.test`, NameServers `127.0.0.1`,
   Comment `Managed by Switchboard; removed by sb uninstall`.
9. `Get-ScheduledTask -TaskPath '\Switchboard\' | Format-List *`: `Daemon-<SID>`,
   State Running, Description the marker. In Task Scheduler (taskschd.msc), its
   trigger is "At log on of <you>", "Run only when user is logged on", not "Run with
   highest privileges".
10. Task Manager → Details: `sb.exe` runs as you, not elevated (the "Elevated" column
    says No), under a `conhost.exe`. **No visible console window.**
11. `certlm.msc` → Trusted Root Certification Authorities → Certificates:
    "Switchboard Local CA". Open it: Name Constraints permits `.test`, Enhanced Key
    Usage is Server Authentication only, and on the Details tab "Edit Properties..."
    shows only Server Authentication enabled. `certmgr.msc` (your user store) doesn't list it.
12. `Get-NetTCPConnection -State Listen -LocalPort 53,80,443`: 127.0.0.1 (and ::1 for
    80/443) owned by the `sb.exe` PID. `Get-NetUDPEndpoint -LocalPort 53`: 127.0.0.1.
13. `Resolve-DnsName anything.test` and `Resolve-DnsName deep.sub.anything.test`:
    127.0.0.1. `ping -n 1 anything.test` too. `Resolve-DnsName example.com` still works
    (the rest of DNS is untouched).
14. `sb doctor`: no FAIL lines. `sb ls`: no DNS or proxy warnings.

**Routing and HTTPS**
15. `python -m http.server 3000` in another window (or any app on 3000), then
    `sb add myapp 3000`. `curl.exe -sI http://myapp.test/` → 307 to https (or 200 if
    you added it without HTTPS). `curl.exe -sI https://myapp.test/` → 200 with no
    certificate error (curl.exe uses Schannel, i.e. the Windows store).
16. Edge and Chrome: https://myapp.test shows the page with a padlock, and the
    certificate chain ends at Switchboard Local CA.
17. Firefox: about:config → `security.enterprise_roots.enabled` is true; https://myapp.test
    loads with no warning. If it's false, set it and note the Firefox version.
18. Stop the Python server: https://myapp.test shows a 502 page naming port 3000.
19. `sb open myapp` opens the default browser. The dashboard at
    https://switchboard.test works (`sb dashboard`).

**Daemon lifecycle**
20. Log off and on: the daemon is back (`sb ls` works) with no window. Restart the VM:
    same.
21. `Stop-ScheduledTask -TaskPath '\Switchboard\' -TaskName "Daemon-$((whoami /user /fo csv | ConvertFrom-Csv).SID)"`:
    `sb.exe` is gone from Task Manager, not orphaned, and `sb ls` says the daemon isn't
    running. `Start-ScheduledTask` (same arguments) brings it back.
22. Kill `sb.exe` in Task Manager: the task restarts it within about a minute.
23. Sleep and resume the VM: `Resolve-DnsName myapp.test` and HTTPS still work.

**Port conflicts (IIS VM or snapshot)**
24. Before `sb setup` (or after stopping the task):
    `Enable-WindowsOptionalFeature -Online -FeatureName IIS-WebServerRole` in an
    elevated shell. `sb doctor`: `[FAIL] port 80: held by http.sys: request queue
    "DefaultAppPool" ...`, with the IIS fix. The pid is `w3wp.exe`'s once a request
    has started it.
    Compare with `netsh http show servicestate view=requestq`.
25. Add an HTTPS binding on 443 in IIS Manager: port 443 is named the same way.
26. `iisreset /stop` (elevated), start the task: ports are the daemon's, `sb doctor`
    passes. `iisreset /start`: IIS fails to bind or the daemon keeps them; either way
    `sb doctor` explains who holds what.
27. Port 53: in a normal PowerShell,
    `$l = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 53); $l.Start()`
    before the daemon starts. `sb doctor`: `[FAIL] port 53: held by powershell (pid N)`.
    `$l.Stop()`.

**Control pipe security**
28. `[IO.Directory]::GetFiles('\\.\pipe\') | Select-String switchboard`: one pipe,
    `switchboard-<16 hex>`, with no SID in its name. Create a second, standard local
    user, sign in as them (fast user switching), and connect to your pipe:
    `$c = [IO.Pipes.NamedPipeClientStream]::new('.', 'switchboard-<id>'); $c.Connect(2000)`
    fails with access denied. Their `sb ls` says their own daemon isn't running; it
    doesn't show your routes. Run the same as an elevated administrator: also denied.
29. Squatting: sign out the second user, stop your task, then as the second user
    create a pipe with your pipe's name:
    `$s = [IO.Pipes.NamedPipeServerStream]::new('switchboard-<id>', 'InOut', 10); $s.WaitForConnection()`.
    As you, `sb ls` refuses: the pipe is served by another user, named as
    `DOMAIN\user (SID)`. `Start-ScheduledTask`: the daemon fails to claim the pipe and
    says why, naming the account (see the task's history / `sb daemon` in a terminal).
    `sb doctor`: `[FAIL] daemon: ...`, saying to delete `pipe-id`. Delete it and
    `Start-ScheduledTask`: the daemon starts on a new pipe name while the squatter
    still runs, and `sb ls` works.

**install.ps1**
30. Windows PowerShell 5.1:
    `irm https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.ps1 | iex`
    (once a release exists and the repository is public; before that, run a local
    copy with `$env:SB_VERSION` set to an existing tag). It verifies the checksum,
    installs to `%LOCALAPPDATA%\Programs\switchboard\sb.exe`, adds it to your user
    PATH, and `sb --version` works in the same window and in a new one.
31. Same in pwsh 7. Running it again with the daemon running succeeds, and leaves the
    previous version as `sb.old.exe`.
32. `$env:SB_VERSION = 'v0.0.0-nope'` then the `irm | iex` line: a clear error, and
    **the PowerShell window stays open**. `$env:SB_VERSION = 'x;calc'`: "not a release
    tag". Remove the variable after.
33. `.\install.ps1 -Global` in a normal shell: refuses and says to use an elevated
    PowerShell. In an elevated one: installs to `C:\Program Files\Switchboard` and the
    machine PATH.
34. Edit one hex digit of the zip's line in a local `SHA256SUMS` copy and serve it
    (or run `test/install/install-test.ps1`): "checksum mismatch", nothing installed.

**Over-the-shoulder elevation**
35. As the standard user from step 28, with a fresh snapshot of their setup:
    `sb setup` and type the administrator's credentials in UAC. The task is
    `Daemon-<standard user's SID>`, runs as the standard user, and the log was
    printed. Running the elevated helper with a `--sid` that isn't the standard
    user's is refused ("is not the SID of"). As that user, create a symbolic link or hard link named
    `%TEMP%\sb-helper-x.log` pointing at a file only admins can write, and run
    From an elevated shell, run
    `sb helper trust --log <that path> --ca-cert "$env:APPDATA\switchboard\pki\ca\ca.pem" --ca-fingerprint <fingerprint from sb trust --print-plan> --uid -1 --user <DOMAIN\name> --sid <SID>`.
    It still works (trust is idempotent), and the target file is unchanged.

**Uninstall**
36. `sb uninstall`, **Yes** in UAC. Then step 2's baseline commands show nothing:
    no rule, no task and no `\Switchboard` folder in Task Scheduler, no certificate,
    no listener. No `sb.exe` process is left. `%APPDATA%\switchboard` still has
    `routes.toml` and the CA files.
37. `sb uninstall` again: succeeds and says everything was already gone.
38. Foreign objects are left alone: add an NRPT rule for `.test` yourself
    (`Add-DnsClientNrptRule -Namespace .test -NameServers 10.0.0.1`, elevated), then
    `sb setup`: it refuses, saying another tool owns `.test`. `sb uninstall` leaves
    your rule. Same with a task you create at `\Switchboard\Daemon-<SID>` with another
    description. Remove both by hand afterwards.
39. Restore the VM snapshot.
