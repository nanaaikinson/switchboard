# macOS setup and uninstall

`sb setup` makes the one-time system changes that let `.test` names work. `sb uninstall`
reverses them. Both print every change and ask for confirmation. Each then runs exactly
one `sudo` command: `sb helper install` or `sb helper uninstall`.

## What `sb setup` changes

| # | Change | Owner and mode | Removed by `sb uninstall` |
| --- | --- | --- | --- |
| 1 | `/etc/resolver/test` sends `.test` lookups to `127.0.0.1` port `15353` | root:wheel 0644 | Yes, but only if it still starts with the Switchboard marker line |
| 2 | `/Library/PrivilegedHelperTools/dev.switchboard.helper`: a copy of `sb` | root:wheel 0755 | Yes |
| 3 | `/Library/LaunchDaemons/dev.switchboard.helper.plist`, loaded into the `system` domain | root:wheel 0644 | Yes, unloaded first |
| 4 | `~/Library/LaunchAgents/dev.switchboard.daemon.plist`, which runs `sb daemon` at login, loaded into `gui/<uid>` | you, 0644 | Yes, unloaded first |

At runtime the jobs also create files, which uninstall removes as well:
- `/var/run/switchboard/helper.sock`: owned by you, mode 0600, in a root-owned directory.
- `/var/log/switchboard-helper.log`.
- `~/Library/Logs/switchboard-daemon.log`.
- `/etc/resolver` itself, if Switchboard created it and it's empty when you uninstall.

Your routes in `~/.config/switchboard` are kept. Delete that folder to remove them too.

The resolver file:

```
# Managed by Switchboard; removed by 'sb uninstall'
nameserver 127.0.0.1
port 15353
```

**The marker line** is how uninstall knows the file is Switchboard's. Other local DNS
tools also write `nameserver 127.0.0.1` files, and those are never removed. If
`/etc/resolver/test` already exists without the marker, `sb setup` stops and says so.

## Security design

- **The helper does very little.** `sb helper serve` binds `127.0.0.1:80` and
  `[::1]:80`, and hands the listening sockets to your daemon. It never accepts
  connections itself. `sb helper install` and `sb helper uninstall` write and remove
  only the files listed above.
- **Only you can talk to the helper.** Its socket is created root-owned, then chowned
  to your uid with mode 0600, inside a root-owned 0755 directory. It accepts exactly
  one request, `{"version":1,"op":"listeners"}`. The socket descriptors are sent with
  `SCM_RIGHTS`.
- **The root job never runs a binary you can modify.** The LaunchDaemon runs a
  root-owned copy of `sb`, not the one in your `PATH`. Re-run `sb setup` after
  upgrading `sb` to refresh that copy.
- **Writing into your home as root is guarded.**
  - `~`, `~/Library` and `~/Library/LaunchAgents` must be real directories owned by
    you, not symlinks.
  - Files are written to an `O_EXCL` temp file, chowned and chmodded through the open
    handle, then renamed into place, so a symlink at the destination is replaced rather
    than followed.
- **`sb setup` refuses to run as root,** so it always installs for the real user.
- **Protocol versions are checked.** If the helper and daemon disagree, the error says
  to re-run `sb setup`.

## How the daemon gets port 80

Unless `--http-addr` is given, `sb daemon` asks the helper for the listeners. If the
helper isn't there, it binds `--http-addr` (default `127.0.0.1:80,[::1]:80`) itself,
and reports any bind error in `sb ls`.

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
   port 15353. This proves the marker comment line parses.
10. `dscacheutil -q host -a name anything.test` should return 127.0.0.1. So should
    `ping -c1 deep.sub.anything.test`.
11. Check the socket and port 80:
    ```bash
    ls -l /var/run/switchboard/helper.sock; sudo lsof -nP -iTCP:80 -sTCP:LISTEN
    ```
    The socket should be `srw-------` and owned by you. Port 80 should be held by both
    `dev.switchboard.helper` (root) and `sb` (you).
12. `sb ls` should print no warnings about DNS or the proxy.

**Routing**
13. Start an app with `python3 -m http.server 3000 &`, then `sb add myapp 3000`.
    `curl -sI http://myapp.test/` should return 200, and `sb open myapp` should open
    Safari.
14. Stop the Python server. `curl http://myapp.test/` should return a 502 page that
    names port 3000.

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
    unchanged. `sb uninstall` should warn "left /etc/resolver/test in place" and keep
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
