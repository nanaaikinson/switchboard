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
| Subject | `CN=Switchboard Local CA, O=Switchboard, OU=uid <uid>` (a SID on Windows) |
| Basic constraints | CA, path length 0: it can sign leaf certificates only, never another CA |
| Extended key usage | TLS server authentication only |
| Name constraints (critical) | permitted DNS: each TLD (`test`); excluded IP: `0.0.0.0/0` and `::/0`; permitted email and URI: `invalid` only |

**Name constraints and the key usage are the main safeguards.** If the key were stolen,
it still couldn't issue a certificate that browsers accept for `google.com` or an IP
address: one signed with the key fails verification with "not authorized to sign for
this name". And because the CA itself is limited to TLS server authentication, which
verifiers apply to everything below it, the key can't sign code (Authenticode), mail
(S/MIME) or client certificates either. The TLDs are fixed when the CA is created, and
only TLDs reserved for local use are allowed: `test`, `local`, `localhost`, `internal`,
`example` and `invalid`. To add a TLD later, make a new CA (see below).

The `OU` names the user who made the CA. The helper only trusts a CA tagged with the
user it runs for, and removing trust finds that user's CAs in the system store by it,
so a deleted or replaced `ca.pem` can't leave a CA trusted.

CAs made by earlier versions have no extended key usage and are tagged
`OU=<login>@<host>`. They keep working, but `sb doctor` flags them, and the next
`sb trust` or `sb setup` replaces them: it makes a new CA, has the helper trust it and
remove the old one from the system store, moves your NSS stores over, and restarts the
daemon.

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

| | System store | NSS (Firefox; Chrome on Linux) |
| --- | --- | --- |
| macOS | `/Library/Keychains/System.keychain`, trusted for SSL and basic X.509, via [smallstep/truststore](https://github.com/smallstep/truststore) (`security add-trusted-cert`). Removal uses `security remove-trusted-cert` and `security delete-certificate` | every Firefox profile with a `cert9.db`; `certutil` from `brew install nss` |
| Linux | the distro CA bundle, via truststore: `/usr/local/share/ca-certificates` + `update-ca-certificates` (Debian, Ubuntu), `/etc/pki/ca-trust/source/anchors` + `update-ca-trust` (Fedora, RHEL), or `trust extract-compat` (Arch). On Debian-family systems removal also runs `update-ca-certificates --fresh`, which clears the links it would leave behind | `~/.pki/nssdb` (Chrome, Chromium) and Firefox profiles, including the snap's; `certutil` from `libnss3-tools` or `nss-tools` |
| Windows | `LocalMachine\Root` ("Trusted Root Certification Authorities" for every user), written directly with crypt32 (`CertAddCertificateContextToStore`). truststore only writes `CurrentUser\Root`, which shows a confirmation dialog per user and can't be done from the elevated setup step. Removal deletes every copy with the same bytes | none: Firefox reads the Windows store (`security.enterprise_roots.enabled`), and Chrome and Edge use it directly |
| Who changes it | `sudo sb helper trust` / `untrust`, run by `sb trust`, `sb untrust`, `sb setup` and `sb uninstall` | the `sb` command itself, as you. Skipped when there are no NSS databases |

On macOS, Safari, Chrome, curl and Go programs use the system store. On Linux, curl,
Go and most CLI tools use the system bundle, while Chrome and Firefox use NSS. The helper validates the
certificate before trusting it; see the security design in
[setup-macos.md](setup-macos.md).

On Linux the anchor file is named `switchboard-<serial in hex>`, never after the
certificate's subject. On Windows the certificate's store entry is also limited to
server authentication (`CERT_ENHKEY_USAGE_PROP_ID`). Trusting a CA first removes the
user's older Switchboard CAs from the system store; removing trust removes all of
them, whether or not their files still exist.

`sb untrust` removes trust but keeps the CA files, so `sb trust` restores the same CA.
On Windows, `sb trust` and `sb untrust` go through one UAC prompt instead of `sudo`.

## Making a new CA

CAs from earlier versions are replaced automatically by `sb trust` (see above).
Otherwise, to replace the CA (because the TLDs changed, it's near expiry, or you think
the key leaked):

```bash
sb untrust
```

Then move `~/.config/switchboard/pki` aside, run `sb trust`, and restart the daemon so it
loads the new CA: `launchctl kickstart -k gui/$(id -u)/dev.switchboard.daemon`. Leaves
are reissued automatically.

## Checking it

`sb doctor` has two HTTPS checks:

- `local CA`: the CA loads, covers every TLD, is limited to TLS server certificates, and
  has more than 90 days left.
- `https probe.<tld>`: a real TLS handshake with the HTTPS proxy, verified against the
  system trust store.
