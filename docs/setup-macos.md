# macOS setup and uninstall

`sb setup` makes the one-time system changes that let `.test` names work. `sb uninstall`
reverses them. Both print every change and ask for confirmation. Each then runs exactly
one `sudo` command: `sb helper install` or `sb helper uninstall`. `sb trust` and
`sb untrust` do the CA steps (5 and 6 below) on their own, with `sb helper trust` and
`sb helper untrust`. See [https.md](https.md) for the CA itself.

All four take the same flags:

| Flag | Does |
| --- | --- |
| `--yes`, `-y` | don't ask for confirmation |
| `--print-plan` | print the changes and the exact command as JSON, and change nothing. The tray app's wizard shows this. |
| `--admin-dialog` | ask for the password in the system's dialog instead of `sudo` in the terminal: `osascript … with administrator privileges` on macOS, `pkexec` on Linux. It's for apps with no terminal, such as the [tray app](tray.md). |

## What `sb setup` changes

| # | Change | Owner and mode | Removed by `sb uninstall` |
| --- | --- | --- | --- |
| 1 | `/etc/resolver/test` sends `.test` lookups to `127.0.0.1` port `535`, which the helper binds for the daemon | root:wheel 0644 | Yes, but only if it still starts with the Switchboard marker line |
| 2 | `/Library/PrivilegedHelperTools/dev.switchboard.helper`: a copy of `sb` | root:wheel 0755 | Yes |
| 3 | `/Library/LaunchDaemons/dev.switchboard.helper.plist`, loaded into the `system` domain | root:wheel 0644 | Yes, unloaded first |
| 4 | `~/Library/LaunchAgents/dev.switchboard.daemon.plist`, which runs `sb daemon` at login, loaded into `gui/<uid>` | you, 0644 | Yes, unloaded first |
| 5 | The local CA certificate is added to `/Library/Keychains/System.keychain` and trusted for TLS server certificates | System keychain | Yes: its trust setting is removed and it is deleted from the keychain |
| 6 | As you, not root: the CA is added to Firefox's NSS databases, if Firefox and `certutil` are installed | your profile | Yes |

Before step 1, `sb setup` creates the CA in `~/.config/switchboard/pki` if it doesn't
exist yet. It runs as you, so the CA files are yours.

At runtime the jobs also create files, which uninstall removes as well:
- `/var/run/switchboard/helper.sock`: owned by you, mode 0600, in a root-owned directory.
- `/var/log/switchboard-helper.log`.
- `~/Library/Logs/switchboard-daemon.log`.
- `/etc/resolver` itself, if Switchboard created it and it's empty when you uninstall.

Your routes and the CA files in `~/.config/switchboard` are kept, but the CA is no longer
trusted. Delete that folder to remove them too.

The resolver file:

```
# Managed by Switchboard; removed by 'sb uninstall'
nameserver 127.0.0.1
port 535
```

**The marker line** is how uninstall knows the file is Switchboard's. Other local DNS
tools also write `nameserver 127.0.0.1` files, and those are never removed. If
`/etc/resolver/test` already exists without the marker, `sb setup` stops and says so.

## Security design

- **The helper does very little.** `sb helper serve` binds ports 80 and 443 on
  `127.0.0.1` and `[::1]`, and the DNS port 535 (UDP and TCP) on `127.0.0.1`, and hands
  the sockets to your daemon. Because they are privileged ports, no other user can
  bind them first. It never
  accepts connections itself. `sb helper install` and `sb helper uninstall` write and
  remove only the files listed above. `sb helper trust` and `sb helper untrust` only
  change the CA's entry in the System keychain.
- **Root only trusts a constrained CA, and only the one you confirmed.** `sb helper
  trust` opens the certificate without following symlinks and refuses anything that
  isn't a self-signed Switchboard CA limited to TLS server certificates, whose critical
  name constraints allow only TLDs reserved for local use (`.test`, `.local`, ...) and
  exclude every IP address. It must also carry your uid and have the SHA-256
  fingerprint `sb trust` showed you (passed as `--ca-fingerprint`). So a process that
  swaps the file in your config dir while you confirm can't make root trust its CA,
  let alone one for real domains.
- **Root's temp files go in `/tmp`.** The helper sets `TMPDIR=/tmp` (sticky, so no one
  else can replace its files) before writing the copies of the certificate that
  `security` reads.
- **Trust changes run in your Terminal, not in launchd.** macOS asks for approval before
  changing admin trust settings, and refuses without a user session. That's why
  trusting goes through `sudo sb helper trust`, like setup, and not through the helper's
  socket. NSS databases are changed as you, so root never writes into your Firefox
  profile.
- **Only you can talk to the helper.** Its socket is created root-owned, then chowned
  to your uid with mode 0600, inside a root-owned 0755 directory, and the helper also
  checks each caller's uid (`LOCAL_PEERCRED`, `SO_PEERCRED` on Linux). It accepts exactly
  one request, `{"version":1,"op":"listeners"}`. The socket descriptors are sent with
  `SCM_RIGHTS`.
- **The root job never runs a binary you can modify.** The LaunchDaemon runs a
  root-owned copy of `sb`, not the one in your `PATH`. Re-run `sb setup` after
  upgrading `sb` to refresh that copy.
- **Writing into your home as root is guarded.**
  - `~`, `~/Library` and `~/Library/LaunchAgents` must be real directories owned by
    you, not symlinks, checked through the open directory handle.
  - Every change in your home goes through an `os.Root` opened on it, so no path can
    lead out of it, even if a directory is swapped for a symlink after it was checked.
  - Files are written to an `O_EXCL` temp file, chowned and chmodded through the open
    handle, then renamed into place, so a symlink at the destination is replaced rather
    than followed.
- **`sb setup` refuses to run as root,** so it always installs for the real user.
- **Protocol versions are checked.** If the helper and daemon disagree, the error says
  to re-run `sb setup`.
- **An out-of-date helper is flagged.** The helper runs its own copy of `sb`, which
  `sb self-update` doesn't replace. It reports its version, and `sb doctor` fails the
  helper check until `sb setup` has updated it, so fixes to the privileged part aren't
  silently missed.

## How the daemon gets ports 80 and 443

Unless `--http-addr` or `--https-addr` is given, `sb daemon` asks the helper for the
listeners, and uses the port 443 ones for HTTPS. It binds any set the helper didn't
send itself: `--http-addr` (default `127.0.0.1:80,[::1]:80`) and `--https-addr`
(default `127.0.0.1:443,[::1]:443`). Bind errors show up in `sb ls` and `sb doctor`. A
helper installed by an older `sb` only sends port 80, so re-run `sb setup` after
upgrading.

## Diagnosing problems: `sb doctor`

`sb doctor` checks every part of the setup without changing anything, prints one
`[PASS]`, `[FAIL]` or `[SKIP]` line per check, and a one-line `fix:` under each failure.
It exits with status 1 if any check fails.

| Check | Passes when | Typical fix |
| --- | --- | --- |
| `daemon` | the control socket answers `GET /v1/status` | re-run `sb setup`, or run `sb daemon` in a terminal to see why it stops |
| `helper` | `/var/run/switchboard/helper.sock` accepts a connection (doctor connects and hangs up) | `sb setup` |
| `resolver .<tld>` | `/etc/resolver/<tld>` is Switchboard's file and names the daemon's DNS port (Linux: see [setup-linux.md](setup-linux.md)) | `sb setup`; if another tool wrote it, remove that file or tool first |
| `probe.<tld>` | `dscacheutil -q host -a name probe.<tld>` returns 127.0.0.1 | fix the checks above, or flush the DNS cache |
| `local CA` | the CA in `~/.config/switchboard/pki` loads, can sign every TLD, and has more than 90 days left | `sb trust` if it's missing; otherwise make a new one (see [https.md](https.md)) |
| `https probe.<tld>` | a TLS handshake with the HTTPS proxy for `probe.<tld>` verifies against the **system** trust store, as a browser would | `sb trust` |
| `port 80`, `port 443` | the daemon's proxy serves the port, or the port is free and the proxy doesn't use it | stop the process named in the failure |
| one line per route | the route's upstream `127.0.0.1:<port>` accepts a TCP connection; for a wildcard route, `sb-probe.<name>` also resolves to 127.0.0.1 | start the app, or `sb add <name> <port>`; for wildcards, a split-DNS mode that supports them |

Port holders are found with `lsof`, which can't see root-owned processes without sudo.
If a port is busy and `lsof` can't name the holder, doctor says so and prints the
`sudo lsof` command to run yourself. While the daemon is down, route checks read the
config file instead, and a busy port fails only if its holder can be named, because the
helper keeps port 80 open between daemon restarts.

## Manual test checklist (macOS VM only)

Never run these on your main machine. Use a VM (UTM, Parallels, or a cloud Mac), take a
snapshot first, and run everything from Terminal in a logged-in GUI session, not over
SSH, so that `gui/<uid>` exists.

**Prepare**
1. On the host: `GOOS=darwin GOARCH=arm64 go build -o sb ./cmd/sb`. Copy it to the VM
   at `~/bin/sb` and put it on your `PATH`.
2. Record a baseline:
   ```bash
   ls -la /etc/resolver; sudo lsof -nP -iTCP:80 -sTCP:LISTEN; launchctl print system/dev.switchboard.helper; launchctl print gui/$(id -u)/dev.switchboard.daemon
   ```
   Expect: no `/etc/resolver/test`, nothing on port 80, and both jobs "Could not find
   service".

**Refusals**
3. `sudo sb setup` should refuse with "run 'sb setup' as your normal user".
4. `sb setup`, answer `n`: it prints the 4 changes and the exact `sudo` command, says
   "Aborted; nothing was changed", and the baseline from step 2 is unchanged.

**Install**
5. `sb setup`, answer `y`: you get one password prompt, then "Setup complete".
6. Check the files:
   ```bash
   cat /etc/resolver/test; ls -l /etc/resolver/test /Library/PrivilegedHelperTools/dev.switchboard.helper /Library/LaunchDaemons/dev.switchboard.helper.plist ~/Library/LaunchAgents/dev.switchboard.daemon.plist
   ```
   The owners and modes should match the table above.
7. Lint the plists and compare the helper copy:
   ```bash
   plutil -lint /Library/LaunchDaemons/dev.switchboard.helper.plist ~/Library/LaunchAgents/dev.switchboard.daemon.plist && cmp ~/bin/sb /Library/PrivilegedHelperTools/dev.switchboard.helper
   ```
   Both plists should print OK, and `cmp` should print nothing.
8. Both jobs should report `state = running`:
   ```bash
   sudo launchctl print system/dev.switchboard.helper | grep state; launchctl print gui/$(id -u)/dev.switchboard.daemon | grep state
   ```
9. `scutil --dns | grep -B1 -A4 'domain   : test'` should show nameserver 127.0.0.1,
   port 535. This proves the marker comment line parses.
10. `dscacheutil -q host -a name anything.test` should return 127.0.0.1. So should
    `ping -c1 deep.sub.anything.test`.
11. Check the socket and port 80:
    ```bash
    ls -l /var/run/switchboard/helper.sock; sudo lsof -nP -iTCP:80 -sTCP:LISTEN
    ```
    The socket should be `srw-------` and owned by you. Port 80 should be held by both
    `dev.switchboard.helper` (root) and `sb` (you).
12. `sb ls` should print no warnings about DNS or either proxy, and `sb doctor` should
    end with "All 9 checks passed."

**Routing**
13. Start an app with `python3 -m http.server 3000 &`, then `sb add myapp 3000`.
    `curl -sI http://myapp.test/` should return 200, and `sb open myapp` should open
    Safari.
14. Stop the Python server. `curl http://myapp.test/` should return a 502 page that
    names port 3000.

**HTTPS**
14a. Keychain Access → System should list "Switchboard Local CA" as trusted ("This
    certificate is marked as trusted for all users"). `curl -sI https://myapp.test/`
    should return 200 with no `-k`, and `curl -sI http://myapp.test/` a 307 to
    `https://myapp.test/`. Safari and Chrome should show a padlock with no warning.
    `curl -sI --http2 https://myapp.test/` should report `HTTP/2`.
14b. `sb add plain 3000 --no-redirect`: `curl -sI http://plain.test/` should return 200.
14c. `sb add '*.tenants.myapp' 3000`: `openssl s_client -connect 127.0.0.1:443 -servername a.tenants.myapp.test </dev/null 2>/dev/null | openssl x509 -noout -ext subjectAltName`
    should show `DNS:*.tenants.myapp.test`.
14d. Name constraints: `openssl x509 -in ~/.config/switchboard/pki/ca/ca.pem -noout -ext nameConstraints`
    should list `Permitted: DNS:test` and, under `Excluded`, `IP:0.0.0.0/0.0.0.0` and
    `IP:0:0:0:0:0:0:0:0/0:0:0:0:0:0:0:0`.
14e. With Firefox installed and `brew install nss`, `sb trust` should add the CA to
    Firefox (Settings → Certificates → Authorities). Quit Firefox first.
14f. `sb untrust`: one password prompt, and `curl https://myapp.test/` should now fail
    with a certificate error. `sb doctor` should report `[FAIL] https probe.test:
    certificate not trusted`. `sb trust` should bring it back.

**Access control**
15. Another user must not be able to reach the helper. Either of these should fail with
    permission denied:
    - `sudo -u nobody nc -U /var/run/switchboard/helper.sock`
    - As a second macOS user: `nc -U /var/run/switchboard/helper.sock`

**Restarts**
16. `launchctl kickstart -k gui/$(id -u)/dev.switchboard.daemon`: `sb ls` should work
    again within a few seconds, with no proxy warning.
17. `kill -9` the `sb daemon` process: launchd should restart it (`KeepAlive`).
18. `sudo launchctl kickstart -k system/dev.switchboard.helper`, then restart the daemon
    as in step 16. Record what happens. The old daemon may still hold port 80 when the
    new helper tries to bind it. The helper and daemon should both recover without a
    reboot; if they don't, file a bug with the log files.
19. Reboot. After you log in, step 13's `curl` should work with no manual steps.

**Re-run and conflicts**
20. Run `sb setup` again and answer `y`: it should succeed, with each plist still
    present exactly once.
21. Run `sb uninstall`. Then create a foreign file:
    ```bash
    printf 'nameserver 127.0.0.1\nport 1053\n' | sudo tee /etc/resolver/test
    ```
    `sb setup` should now fail with "not written by Switchboard", leaving the file
    unchanged. `sb doctor` should report `[FAIL] resolver .test: ... written by another
    tool (nameserver 127.0.0.1 port 1053)` and exit 1. `sb uninstall` should warn "left /etc/resolver/test in place" and keep
    the file. Then run `sudo rm /etc/resolver/test`.

**Uninstall**
22. `sb setup` then `sb uninstall` (answer `y`): you get one password prompt, then
    "Switchboard system changes removed."
23. Everything should be gone:
    ```bash
    ls /etc/resolver/test /Library/PrivilegedHelperTools/dev.switchboard.helper /Library/LaunchDaemons/dev.switchboard.helper.plist ~/Library/LaunchAgents/dev.switchboard.daemon.plist /var/run/switchboard /var/log/switchboard-helper.log ~/Library/Logs/switchboard-daemon.log
    ```
    Every path should be "No such file".
    - Both `launchctl print` commands from step 2 should say "Could not find service".
    - `sudo lsof -iTCP:80` should be empty.
    - `scutil --dns` should have no `test` domain.
24. Run `sb uninstall` again: it should succeed without errors, so it's idempotent.
25. Partial state: run `sb setup`, then `rm ~/Library/LaunchAgents/dev.switchboard.daemon.plist`,
    then `sb uninstall`. It should succeed, and step 23's checks should pass.
26. Check that nothing is left behind:
    ```bash
    sudo find / -xdev -iname '*switchboard*' 2>/dev/null
    ```
    Only `~/.config/switchboard` should remain.

**Open question to answer in the VM**
27. Without the helper installed, run `sb daemon --http-addr 127.0.0.1:80` as your
    normal user. macOS 10.14 and later are reported to allow unprivileged binds to
    ports below 1024. If it binds, the root helper may not be needed for port 80 on
    macOS.
