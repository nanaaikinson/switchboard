# HTTPS and the local CA

`internal/pki` runs a small certificate authority for your machine. The daemon uses it
to serve every route over HTTPS, and `sb trust` (or `sb setup`) makes the OS and
browsers trust it.

## The CA

The CA is created the first time `sb daemon`, `sb setup` or `sb trust` needs it.

| Property | Value |
| --- | --- |
| Key | ECDSA P-256 |
| Validity | 10 years |
| Subject | `CN=Switchboard Local CA, O=Switchboard, OU=<user>@<host>` |
| Basic constraints | CA, path length 0: it can sign leaf certificates only, never another CA |
| Name constraints (critical) | permitted DNS: each TLD (`test`); excluded IP: `0.0.0.0/0` and `::/0` |

**Name constraints are the main safeguard.** If the key were stolen, it still couldn't
issue a certificate that browsers accept for `google.com` or an IP address. A
certificate for `google.com` signed with the key fails verification with "not
authorized to sign for this name". The TLDs are fixed when the CA is created. To add a
TLD later, make a new CA (see below).

### Files

They live in `<config dir>/pki` (usually `~/.config/switchboard/pki`, mode 0700):

| File | Mode | Contents |
| --- | --- | --- |
| `ca/ca.pem` | 0644 | CA certificate (public) |
| `ca/ca-key.pem` | 0600 | CA private key, PKCS#8 |
| `leaves/<name>.pem` | 0600 | Cached leaf certificate and key; `*` in a name is stored as `_wildcard` |

The CA is written to a temp directory and renamed into place, so the certificate and
key always match, even if two processes create it at once.

**Why not the macOS Keychain for the key?** Reading a private key back out of the
Keychain needs cgo and the Security framework, or password prompts from the `security`
tool. Neither is acceptable for a daemon that starts at login. The key file is readable
only by you, which is the same protection your SSH keys have.

## Leaf certificates

Leaves are issued on demand during the TLS handshake (`tls.Config.GetCertificate`),
from the SNI server name:

- A name routed exactly (`myapp.test`), or not routed at all, gets a certificate for
  exactly that name. Unrouted names still get one, so the 404 page loads over HTTPS.
- A name matched only through a wildcard route gets a wildcard certificate for its
  parent: `a.tenants.myapp.test` and `b.tenants.myapp.test` share
  `*.tenants.myapp.test`. Wildcards cover one label, so `x.a.tenants.myapp.test` gets
  `*.a.tenants.myapp.test`.
- Names outside the CA's TLDs, names that aren't LDH hostnames, a wildcard for a bare
  TLD (`*.test`), and handshakes without SNI are refused.

Each leaf is ECDSA P-256 and valid for 90 days, with the server-auth extended key
usage. Leaves are cached in memory and on disk, and reissued when less than 30 days are
left, or if they weren't signed by the current CA.

## Trust

| | System store | Firefox (NSS) |
| --- | --- | --- |
| Where | `/Library/Keychains/System.keychain`, trusted for SSL and basic X.509 | every Firefox profile with a `cert9.db` |
| Who changes it | `sudo sb helper trust` / `untrust`, run by `sb trust`, `sb untrust`, `sb setup` and `sb uninstall` | the `sb` command itself, as you |
| How | [smallstep/truststore](https://github.com/smallstep/truststore) (`security add-trusted-cert`); removal uses `security remove-trusted-cert` and `security delete-certificate` | `certutil` from `brew install nss`; skipped when Firefox isn't installed |

Safari, Chrome, curl and Go programs all use the system store. The helper validates the
certificate before trusting it; see the security design in
[setup-macos.md](setup-macos.md).

`sb untrust` removes trust but keeps the CA files, so `sb trust` restores the same CA.
Linux and Windows trust stores aren't implemented yet.

## Making a new CA

There's no automatic rotation yet. To replace the CA (because the TLDs changed, it's
near expiry, or you think the key leaked):

```bash
sb untrust
```

Then move `~/.config/switchboard/pki` aside, run `sb trust`, and restart the daemon so it
loads the new CA: `launchctl kickstart -k gui/$(id -u)/dev.switchboard.daemon`. Leaves
are reissued automatically.

## Checking it

`sb doctor` has two HTTPS checks:

- `local CA`: the CA loads, covers every TLD, and has more than 90 days left.
- `https probe.<tld>`: a real TLS handshake with the HTTPS proxy, verified against the
  system trust store.
