# Linux setup and uninstall

On Linux, `sb setup` and `sb uninstall` work the same way as on
[macOS](setup-macos.md). Each prints every change, asks for confirmation, then runs one
`sudo sb helper install` or `sudo sb helper uninstall`. The Linux implementation lives
in `internal/platform/linux`. The helper protocol and the root-safe file writes are
shared with macOS in `internal/platform/posix`.

## Requirements

- **systemd 248 or later.** The helper is a system unit. The daemon is a unit in your
  systemd user session, managed with `systemctl --user --machine=<you>@`.
- **A running user session.** You get one when you log in to a desktop. On a server or
  over SSH without lingering, run `loginctl enable-linger $USER` once, or setup stops
  and says so.
- **For wildcard routes:** systemd-resolved, or NetworkManager with its dnsmasq plugin.
  Without either, only exact names resolve.
- **Optional:** `certutil` (`libnss3-tools` or `nss-tools`) so Chrome and Firefox
  trust the CA.

## Split DNS: three modes

`sb setup` checks what the machine uses, in this order:

| Mode | Chosen when | What Switchboard writes | Wildcards |
| --- | --- | --- | --- |
| systemd-resolved | `systemctl is-active systemd-resolved` is `active` | `/etc/systemd/resolved.conf.d/switchboard-<tld>.conf`, then restarts systemd-resolved | yes |
| NetworkManager + dnsmasq | `NetworkManager --print-config` has `dns=dnsmasq` | `/etc/NetworkManager/dnsmasq.d/switchboard-<tld>.conf`, then `nmcli general reload dns-full` (or reloads NetworkManager) | yes |
| `/etc/hosts` fallback | neither of the above | a marked block in `/etc/hosts` | **no** |

The drop-ins:

```ini
# Managed by Switchboard; removed by 'sb uninstall'
[Resolve]
DNS=127.0.0.1:15353
Domains=~test
```

```
# Managed by Switchboard; removed by 'sb uninstall'
server=/test/127.0.0.1#15353
```

`Domains=~test` makes `.test` a routing-only domain, so only `.test` lookups go to
Switchboard. Setup refuses to overwrite a drop-in at either path that doesn't start with
the marker line, and uninstall leaves such a file alone.

**A caveat with systemd-resolved:** the drop-in adds a *global* DNS server. If your
`resolved.conf` already sets global `DNS=` servers, systemd-resolved may send `.test`
lookups to those too, and the global scope stops being a default route for other names.
Most desktops configure DNS per link (NetworkManager, systemd-networkd), which isn't
affected. If `sb doctor`'s `probe.test` check fails intermittently, check
`resolvectl status`.

### The `/etc/hosts` fallback

A hosts file can't hold wildcards, so in this mode Switchboard keeps a block with each
exact route name, plus `probe.<tld>` for `sb doctor`:

```
# BEGIN Switchboard tlds=test; removed by 'sb uninstall'
127.0.0.1 myapp.test
::1 myapp.test
# END Switchboard
```

The daemon sends the route names to the helper whenever the routes change. The helper
rewrites only this block, and only if setup created it. It refuses any name that isn't
a plain hostname under a TLD listed in the block's header, and at most 2000 names, so
the daemon can't make root map `google.com`. The file is rewritten in place, not
renamed over it, because containers often bind-mount `/etc/hosts`.

`sb doctor` fails a wildcard route in this mode ("names under it don't resolve"). To
switch modes, enable systemd-resolved, then run `sb uninstall` and `sb setup`.

## What `sb setup` changes

| # | Change | Owner and mode | Removed by `sb uninstall` |
| --- | --- | --- | --- |
| 1 | The split-DNS drop-in or `/etc/hosts` block above | root, 0644 | Yes, only Switchboard's |
| 2 | `/usr/local/libexec/switchboard/sb-helper`: a root-owned copy of `sb` | root, 0755 | Yes, with the directory |
| 3 | `/etc/systemd/system/switchboard-helper.service`, enabled and started | root, 0644 | Yes, stopped and disabled first |
| 4 | `~/.config/systemd/user/switchboard.service`, which runs `sb daemon`, enabled and started in your user session | you, 0644 | Yes, stopped and disabled first |
| 5 | The local CA, added to the distro CA bundle (see [https.md](https.md)) | root | Yes |
| 6 | As you, not root: the CA, added to `~/.pki/nssdb` and Firefox profiles if `certutil` is installed | you | Yes |

The helper's socket, `/run/switchboard/helper.sock`, lives in the unit's
`RuntimeDirectory`, so systemd removes it when the helper stops. Logs go to the journal:
`journalctl -u switchboard-helper` and `journalctl --user -u switchboard`. Your routes
and the CA files in `~/.config/switchboard` are kept.

## Security design

Everything in the [macOS security design](setup-macos.md#security-design) applies:

- the helper only binds ports and hands them over;
- only you can connect to its socket (0600, chowned to you);
- the root service runs a root-owned copy of `sb`;
- root writes into your home only below real directories you own (`~/.config`,
  `~/.config/systemd`, `~/.config/systemd/user` are each checked);
- root only trusts a name-constrained Switchboard CA.

On top of that, the helper's unit is sandboxed to exactly what it does:

```ini
NoNewPrivileges=yes
ProtectSystem=strict          # the whole filesystem is read-only...
ReadWritePaths=-/etc/hosts    # ...except the hosts file (for the fallback)
ProtectHome=yes
PrivateTmp=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_CHOWN
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
```

`sb setup` refuses an `sb` path with spaces or characters that systemd would interpret
in `ExecStart=` (`%`, quotes, `$`, `;`). Move `sb` to a plain path such as
`~/.local/bin/sb`.

## End-to-end test

[test/e2e/linux](../test/e2e/linux) runs the real thing in a privileged Ubuntu 24.04
container that boots systemd, as a normal user `dev` with passwordless sudo. It runs
`sb setup`, then checks that `https://probe.test` and a wildcard route work with a
trusted certificate over HTTP/2, that HTTP redirects, and that `sb doctor` passes. Then
it runs `sb uninstall` and checks that nothing is left: drop-ins, the hosts block,
units, the helper binary, the CA in the bundle, port 443. It does all of that twice:
once with systemd-resolved, and once with the `/etc/hosts` fallback, where it also
checks that `sb doctor` flags the wildcard route.

```bash
make test-e2e-linux
```

It needs Docker and never uses sudo on your machine. CI runs it on every push (the
`e2e-linux` job). `E2E_KEEP=1` keeps the container for debugging.
