# DNS server

`internal/dns` is the resolver the OS uses, through split DNS, for Switchboard TLDs. It
never serves `.local`, which the experimental .local mode announces over multicast DNS
instead ([mdns.md](mdns.md)).

## Behaviour

| Query                                             | Response                          |
| ------------------------------------------------- | --------------------------------- |
| `A` for any name under a served TLD, any depth    | `127.0.0.1`, authoritative, TTL 1 |
| `AAAA` for any name under a served TLD            | `::1`, authoritative, TTL 1       |
| Other types (MX, TXT, HTTPS…) under a served TLD  | `NOERROR`, no answer (NODATA)     |
| The bare TLD itself (`test.`)                     | `NOERROR`, no answer (NODATA)     |
| Anything else (other TLDs, root, non-QUERY opcode, multiple questions, non-IN class) | `REFUSED` |

- Name matching is case-insensitive, and the answer echoes the name as it was asked.
- The server never recurses or forwards queries. `RA` is always clear.
- The TTL is 1 second, so TLD changes take effect quickly even with OS caching.

## Configuration

- **Address:** after `sb setup` on macOS and Linux, `127.0.0.1:535`. That port is
  privileged, so the root helper binds it (UDP and TCP) and hands the sockets to the
  daemon, the way it does for 80 and 443: no other user on the machine can bind it
  first and answer `.test` lookups with addresses of their choosing. Without the
  helper (before setup, or with a helper from an earlier version until you re-run
  `sb setup`) the daemon binds `--dns-addr` itself, by default `127.0.0.1:15353`;
  `sb doctor` then reports that the resolver points at another port. On Windows it is
  `127.0.0.1:53`, bound by the daemon (see [setup-windows.md](setup-windows.md)).
  UDP and TCP bind the same port. The host must be a loopback IP; `:15353`, LAN IPs
  and hostnames are rejected. Port 5353 is avoided on purpose: it is the mDNS port,
  and mDNSResponder, Avahi, browsers and media apps already bind `*:5353`. On macOS,
  anyone may bind a privileged port on the wildcard address, so the helper binds
  `127.0.0.1:535` with `SO_REUSEADDR`: the more specific socket gets the traffic even
  if someone holds `*:535`.
- **TLDs:** defaults to `["test"]`. Each TLD is lowercased and loses its leading and
  trailing dots. TLDs must be LDH labels (letters, digits, hyphens) and may have more
  than one label, such as `dev.internal`.
- **Changing TLDs at runtime:** `Server.SetTLDs` swaps the list atomically while the
  server is running. An invalid list is rejected and the old list stays in place.

## Usage

```go
s, err := dns.New(dns.DefaultAddr, []string{"test"})
if err != nil { ... }
if err := s.Listen(); err != nil { ... }  // bind errors surface here
go s.Serve(ctx)                            // returns when ctx is done
_ = s.SetTLDs([]string{"test", "internal"})
```
