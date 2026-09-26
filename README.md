# Switchboard

Switchboard gives the apps you run on your own computer real, trusted HTTPS names.
Instead of `http://localhost:7000`, you open `https://myapp.test`.

```bash
sb add myapp 7000
sb open myapp          # https://myapp.test, with a certificate your browser trusts
```

It runs in the background on macOS, Linux and Windows. You set it up once. After that,
naming an app takes one command.

> **Status: pre-release.** Switchboard is in its first release candidates
> (`v0.1.0-rc.*`). Expect rough edges, and please report them.

## What it does

- **Names for local ports.** `myapp.test` goes to `127.0.0.1:7000`. Subdomains
  (`api.myapp.test`) and wildcards (`*.tenants.myapp.test`) work too.
- **Trusted HTTPS.** Switchboard runs a small certificate authority on your machine,
  limited to local names, and trusts it for you. No certificate warnings, and no
  `mkcert` step per project.
- **Nothing to configure in your app.** Your app keeps listening on its usual port.
  Switchboard answers the DNS lookup, terminates HTTPS and forwards the request,
  WebSockets and server-sent events included.
- **Project files.** Commit a `switchboard.toml` and everyone on the team gets the same
  names with `sb apply`.
- **Docker.** Running containers that publish a port get a name automatically:
  `web.test`, or `api.shop.test` for a Compose service.
- **A dashboard and a menu-bar app.** See every route and whether its app is up, add
  and remove routes, and watch recent requests.
- **Checks itself.** `sb doctor` tests every part of the setup and says how to fix what
  isn't working.
- **Fully reversible.** `sb uninstall` removes every change `sb setup` made.

Everything stays on your machine. Switchboard only listens on `127.0.0.1` and `::1`,
and only answers for the names it manages (`.test` by default).

## Quick start

1. **Install `sb`.** On macOS or Linux:

   ```bash
   curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.sh | SB_VERSION=v0.1.0-rc.3 sh
   ```

   The script checks the release's signature, so it needs
   [`minisign`](https://jedisct1.github.io/minisign/) (`brew install minisign`, or your
   package manager). Windows, `.deb`/`.rpm` and other options:
   [Installing](docs/install.md).

2. **Set up your system once.** Run it as your normal user, not with `sudo`:

   ```bash
   sb setup
   ```

   It lists every change it will make and asks before making them. You enter your
   password once.

3. **Name an app.** Start your app on a port, then:

   ```bash
   sb add myapp 3000
   sb open myapp
   ```

Something not working? Run `sb doctor`.

## Documentation

**Using Switchboard**
- [Getting started](docs/getting-started.md): install, set up, first route
- [Using Switchboard](docs/usage.md): routes, wildcards, project files, Docker, the
  dashboard, the menu-bar app and updates
- [Troubleshooting](docs/troubleshooting.md): `sb doctor`, common problems and fixes
- [Uninstalling](docs/uninstall.md): remove Switchboard completely

**Reference**
- [Installing](docs/install.md) · [Project files](docs/project-config.md) ·
  [Docker routes](docs/docker.md) · [Dashboard](docs/dashboard.md) ·
  [Menu-bar app](docs/tray.md) · [.local mode](docs/mdns.md) ·
  [Updates](docs/updates.md)
- How it works: [DNS](docs/dns.md) · [Proxy](docs/proxy.md) ·
  [HTTPS and the local CA](docs/https.md) · [Control API](docs/api.md) ·
  [Route file](docs/config.md)
- What setup changes, and why, per system:
  [macOS](docs/setup-macos.md) · [Linux](docs/setup-linux.md) ·
  [Windows](docs/setup-windows.md)

## How it works

`sb setup` makes four changes, all listed before it asks for your password:

1. It sends lookups for `.test` names to Switchboard's DNS server on `127.0.0.1`
   (`/etc/resolver/test` on macOS, a systemd-resolved or dnsmasq drop-in on Linux, an
   NRPT rule on Windows). Everything else still goes to your normal DNS.
2. It installs a small privileged helper (macOS and Linux) that only binds the DNS port
   (535), 80 and 443 on loopback and hands them to the daemon. Besides that, it only
   runs the setup, trust and uninstall steps you confirm.
3. It starts `sb daemon` as you, at login.
4. It trusts Switchboard's local certificate authority. That CA can only sign
   certificates for local names (like `.test`), never for real websites, and only for
   HTTPS servers.

The daemon holds your routes, answers DNS for them, and proxies each request to
`127.0.0.1:<port>`. The `sb` command, the dashboard and the menu-bar app all talk to the
daemon; none of them change system files.

## Supported systems

| System | Status |
| --- | --- |
| macOS 13 and later (Apple silicon and Intel) | Supported |
| Linux with systemd (x86_64 and arm64) | Supported; wildcards need systemd-resolved or NetworkManager's dnsmasq |
| Windows 10 1809+ and 11 | Supported for the `sb` command. The menu-bar app is macOS-only for now |

## Uninstalling

```bash
sb uninstall
```

That removes every system change. Then delete `sb` itself the way you installed it.
[Uninstalling](docs/uninstall.md) has the details for each install method.
