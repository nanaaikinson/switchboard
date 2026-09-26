# .local mode (experimental)

`.test` names resolve through split DNS, which `sb setup` installs. `.local` is
different: it is reserved for multicast DNS (mDNS, RFC 6762), so Switchboard never
installs a resolver for it. Instead, in .local mode the daemon announces each route
under `.local` over mDNS, pointing at `127.0.0.1` and `::1`.

.local mode is **experimental** and off by default. Prefer `.test` unless a tool you
use insists on `.local`.

```sh
sb tld add local --mdns     # turn it on
sb add myapp.local 3000     # announced as myapp.local -> 127.0.0.1, ::1
sb ls                       # the MDNS column shows each .local route's state
sb doctor                   # checks announcements and that .local lookups stay local
sb tld rm local             # turn it off (remove its routes first)
```

## How names are announced

- **Loopback only.** Names are announced on the loopback interface, so other machines
  on the network never see them.
- **Every exact route name** under `.local` is announced when it is added (with
  `sb add`, `sb apply` or Docker), and withdrawn when it is removed, when .local mode is
  turned off, or when the daemon stops. Records have a 10-second TTL, so a crashed
  daemon's names expire quickly.
- **Wildcards can't be announced.** mDNS answers exact names only, so `*.tenants.local`
  never resolves, and a route added with `--wildcard` only announces its own name.
  `sb ls` and the dashboard flag these routes; add each name you need instead
  (`sb add acme.tenants.local 3000`).
- **Nothing on the system changes.** Announcements live in memory, so there is nothing
  for `sb uninstall` to revert. `sb tld add` only writes `[[tlds]]` to
  [routes.toml](config.md).

Responders, tried in order:

| Backend | Where | How |
| ------- | ----- | --- |
| `mDNSResponder` | macOS | Local-only records (`kDNSServiceInterfaceIndexLocalOnly`) registered over mDNSResponder's socket, `/var/run/mDNSResponder`, speaking its client protocol directly (no cgo). mDNSResponder withdraws them when the daemon's connection closes. |
| `Avahi` | Linux | One D-Bus entry group per name on the system bus, pinned to the loopback interface index, without reverse (PTR) records. Needs `avahi-daemon` and, for lookups, `libnss-mdns`. |
| `go` | macOS (fallback), Windows | Built in (hashicorp/mdns). Answers queries and sends announcements and goodbyes on the loopback interface. Resolvers that don't query over loopback won't see it. |

The built-in responder is never used on Linux: Linux delivers multicast to every socket
on port 5353 whatever interface it joined, so its answers could reach the network, and
the loopback interface has no multicast by default. Without Avahi, `.local` names are
not announced there.

When mDNSResponder or avahi-daemon restarts, it forgets the daemon's records; the daemon
notices and announces them again.

If no responder starts, the daemon keeps running, retries every 10 seconds, and reports
the error in `sb ls`, `sb doctor`, the dashboard and `GET /v1/status` (`mdns.error`).

## HTTPS

The local CA is name-constrained to the TLDs it was made for. A CA made while .local
mode is on covers `.local`; an older one only covers `.test`, and HTTPS for `.local`
names fails. `sb tld add` and `sb doctor` say so, and how to make a new CA:
`sb untrust`, move `<config dir>/pki/ca` aside, `sb trust`, then re-run `sb setup` to
restart the daemon with it.

A CA that covers `.local` could, if its key were stolen, sign certificates for any
`.local` device on your network, such as printers. That is one more reason the mode is
opt-in.

## Leaks to unicast DNS

Some networks, VPNs and corporate device profiles send `.local` lookups to a normal DNS
server, which then sees every `.local` name you look up and may answer with another
host's address (older Active Directory domains often use `.local`). Programs that skip
mDNS, such as Go and musl-based programs and `dig`, always ask unicast DNS.

With .local mode on, `sb doctor` checks:

- **macOS:** `scutil --dns` for a resolver for the `local` domain with unicast name
  servers (from `/etc/resolver/local`, a VPN or an MDM profile).
- **Linux:** the `hosts:` line of `/etc/nsswitch.conf`: an mdns module with
  `[NOTFOUND=return]`, or systemd-resolved, must come before `dns`.
- **macOS and Linux:** it asks each server in `/etc/resolv.conf` for a random
  `sb-leak-<hex>.local` name. A server that answers is intercepting `.local`.
- **Windows:** not checked yet (reported as SKIP).

A leak fails the check, with what to change. Until it is fixed, use `.test` names for
anything that must stay on your machine.
