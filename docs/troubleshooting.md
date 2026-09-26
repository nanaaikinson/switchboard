# Troubleshooting

## Start with `sb doctor`

```bash
sb doctor
```

It checks every part of Switchboard and prints one line per check. A failed check is
followed by a `fix:` line that says what to do:

```
[PASS] daemon: running v0.1.0, up 2h
[PASS] helper: running
[FAIL] resolver .test: /etc/resolver/test was written by another tool (nameserver 127.0.0.1 port 1053)
       fix: Another local DNS tool owns .test; remove /etc/resolver/test or that tool (e.g. 'valet uninstall'), then run 'sb setup'.
```

It checks the daemon, the privileged helper, the DNS setting for each TLD, that
`probe.test` resolves to `127.0.0.1`, the certificate authority, a real HTTPS handshake,
ports 80 and 443, and each route's app. It exits with status 1 if anything fails. Checks
your system doesn't support show as `SKIP`.

The rest of this page covers the most common problems.

## `sb setup` says another tool owns `.test`

```
install resolver: /etc/resolver/test exists: not written by Switchboard; another tool (such as Laravel Valet) may own .test. Remove it or use another TLD
```

Another local-DNS tool (Laravel Valet, puma-dev, dnsmasq, and others) already handles
`.test`. Switchboard never overwrites another tool's settings. Either uninstall or
reconfigure that tool, or remove its file (here `/etc/resolver/test`) if you no longer
use it. Then run `sb setup` again. On Linux the same applies to drop-ins in
`/etc/systemd/resolved.conf.d` and `/etc/NetworkManager/dnsmasq.d`; on Windows, to an
NRPT rule for `.test`.

## The browser shows a certificate warning

1. Run `sb doctor` and look at `local CA` and `https probe.test`.
2. Restart the browser. Browsers read the trust store when they start.
3. **Firefox**, and **Chrome on Linux**, use their own certificate store. Switchboard
   adds its certificate authority there only if `certutil` is installed (macOS:
   `brew install nss`; Debian and Ubuntu: `sudo apt install libnss3-tools`; Fedora:
   `sudo dnf install nss-tools`). Install it, then run `sb trust`.
4. If you ran `sb untrust`, run `sb trust`.

## The name doesn't open at all

- **"Server not found" or similar:** your system hasn't picked up the DNS setting yet.
  Flush its cache: on macOS, `sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder`;
  on Linux, `resolvectl flush-caches`. Then run `sb doctor`.
- **A VPN or custom DNS client** may bypass the system's split DNS. Check whether
  `probe.test` resolves with the VPN off.
- **Wildcards on Linux** need systemd-resolved or NetworkManager's dnsmasq. On systems
  with neither, Switchboard falls back to `/etc/hosts`, which only has exact names.
- **Is the name routed?** `sb ls` lists every route. A name with no route shows a
  "No route" page that lists them.

## "Nothing is listening on port 3000"

Switchboard reached your machine, but the app on that port didn't answer.

- Start the app, and check the port with `sb ls`.
- **The app must listen on `127.0.0.1`.** Switchboard connects to `127.0.0.1:<port>`.
  Some dev servers bind only to `::1` (IPv6) when told `localhost`. Start them with the
  host `127.0.0.1` or `0.0.0.0` (for Vite: `vite --host 127.0.0.1`).

## The app rejects the request or redirects in a loop

Your app now sees `Host: myapp.test` and requests forwarded from HTTPS.

- **Allowed hosts:** frameworks that check the host need `myapp.test` (or `.test`)
  allowed: Django `ALLOWED_HOSTS`, Rails `config.hosts`, Vite `server.allowedHosts`.
- **HTTPS redirect loops:** the app receives plain HTTP from Switchboard, with
  `X-Forwarded-Proto: https`. Tell it to trust that header (it's often called "trust
  proxy" or "forwarded headers"), or don't force HTTPS in development.
- **"This route loops back to Switchboard":** the route points at port 80 or 443, where
  Switchboard itself listens. Point it at your app's port.

## Port 80 or 443 is taken

`sb doctor` names the process holding the port, such as nginx, Apache, Laravel Valet or,
on Windows, IIS or another `http.sys` service. Stop it or move it to another port, then
run `sb setup` again.

On Windows, `sb doctor` also warns when another account's process holds port 53, 80 or
443. Windows has no privileged ports, so another user on the same machine can take them
while your daemon is stopped. Have an administrator stop that process.

## Everything answers "Switchboard is paused"

Every route is off. Run `sb resume`, or uncheck **Pause All** in the menu-bar app.

## `sb doctor` says to re-run `sb setup` after an upgrade

After you upgrade `sb`, the privileged helper still runs its own copy of the old
version, and older versions used a different DNS port and certificate authority. Run
`sb setup` again. It updates the helper, points DNS at the new port, and replaces an
older certificate authority, keeping the old one trusted until the new one is.

## The daemon isn't running

`sb doctor` shows `[FAIL] daemon: not running`. Run `sb setup` again to restart its
login service. To see why it stops, look at its log (macOS:
`~/Library/Logs/switchboard-daemon.log`; Linux: `journalctl --user -u switchboard`), or
run `sb daemon` in a terminal and read its output.

## The dashboard says to run `sb dashboard`

The dashboard only opens through a one-time sign-in link. Run `sb dashboard`. The link
works once, within two minutes. You'll need a new one after the daemon restarts.

## The install script refuses to install

- **"minisign is needed":** the script checks the release signature. Install `minisign`
  (`brew install minisign`, or your package manager) and run the script again.
- **No release found:** while Switchboard is in pre-release, set `SB_VERSION`, for
  example `SB_VERSION=v0.1.0-rc.3`. Without it, the script looks for the latest stable
  release.
- **Checksum or signature mismatch:** don't bypass it. Try again later, and report it if
  it keeps happening.

## `.local` names (experimental)

`sb doctor` checks that each `.local` name is announced and resolves, and that `.local`
lookups don't leak to your network's DNS server. Wildcard routes can't be announced
over mDNS: add each `.local` name. See [.local mode](mdns.md).

## Common questions

**Is it safe?** Switchboard only listens on `127.0.0.1` and `::1`, so nothing on your
network can reach it. Its certificate authority can only sign certificates for local
names (`.test`, `.local`), only for HTTPS servers, and never for real websites or IP
addresses. The privileged helper only binds ports and runs the setup steps you confirm.
Details: [HTTPS and the local CA](https.md) and the security sections of
[setup-macos.md](setup-macos.md).

**Why `.test`?** It's reserved for testing (RFC 2606), so it can never clash with a real
website.

**Does it work offline?** Yes. Nothing leaves your machine.

**Can I use it next to Valet, puma-dev or dnsmasq?** Not for the same TLD. Each tool
needs to own its TLD's DNS setting. Use one tool per TLD.

**How do I get help?** Open an issue on
[GitHub](https://github.com/nanaaikinson/switchboard/issues) with what you ran and the
output of `sb doctor`.
