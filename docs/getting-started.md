# Getting started

This takes about five minutes. You install the `sb` command, run `sb setup` once, then
give your first app a name.

## What you need

- macOS 13 or later, Linux with systemd, or Windows 10 (1809 or later) or 11.
- An administrator password. `sb setup` asks for it once.
- An app that listens on a local port, such as a dev server on `localhost:3000`.
- For Firefox (and Chrome on Linux) to trust the certificates: `certutil`. On macOS,
  `brew install nss`. On Debian and Ubuntu, `sudo apt install libnss3-tools`. On Fedora,
  `sudo dnf install nss-tools`. Other browsers use the system trust store and need
  nothing.

## 1. Install sb

**macOS and Linux:**

```bash
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.sh | SB_VERSION=v0.1.0-rc.3 sh
```

The script downloads the release for your system, checks its signature and checksum,
and installs `sb` to `~/.local/bin`. It needs `minisign` to check the signature: install
it first with `brew install minisign` or your package manager. If `~/.local/bin` isn't
on your `PATH`, the script prints the line to add to your shell profile.

**Windows,** in PowerShell as your normal user:

```powershell
$env:SB_VERSION = 'v0.1.0-rc.3'; irm https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.ps1 | iex
```

It installs `sb.exe` to `%LOCALAPPDATA%\Programs\switchboard` and adds that folder to
your `PATH`. Open a new terminal afterwards.

While Switchboard is in pre-release, pass `SB_VERSION` as above: without it, the scripts
look for the latest *stable* release, and there isn't one yet. Homebrew, `.deb`/`.rpm`,
Scoop and winget: see [Installing](install.md).

Check it worked:

```bash
sb --version
```

## 2. Set up your system

Run this once, as your normal user. Don't use `sudo`:

```bash
sb setup
```

`sb setup` prints every change it will make, then asks `Continue? [y/N]`. Nothing
changes until you answer `y`. Then it asks for your password once (macOS asks again in a
dialog before it trusts the certificate authority). On Windows, you get one UAC prompt.

What it changes, in short:

- `.test` lookups go to Switchboard's DNS server on `127.0.0.1`. Other lookups are
  untouched.
- A small privileged helper binds ports 80, 443 and 535 on loopback for the daemon
  (macOS and Linux).
- `sb daemon` starts as you, now and at every login.
- Switchboard's local certificate authority is trusted, so HTTPS works without
  warnings.

The full list, per system, with every file path: [macOS](setup-macos.md),
[Linux](setup-linux.md), [Windows](setup-windows.md). To see the plan without changing
anything: `sb setup --print-plan`.

If a browser was open during setup, restart it so it picks up the new certificate
authority.

## 3. Name your first app

Start your app, say on port 3000. Then:

```bash
sb add myapp 3000
sb open myapp
```

Your browser opens `https://myapp.test`, with a valid certificate. `sb add` adds `.test`
for you, so `myapp` and `myapp.test` are the same.

List your routes and whether their apps are up:

```bash
sb ls
```

```
NAME         PORT  UPSTREAM  SOURCE
myapp.test   3000  up        config
```

## 4. Check everything

```bash
sb doctor
```

It checks the daemon, the helper, DNS, the certificate authority, HTTPS, the ports, and
each app. Every failed check comes with a one-line fix. All `PASS`? You're done.

## Next steps

- Subdomains, wildcards, project files, Docker, the dashboard and more:
  [Using Switchboard](usage.md).
- Something isn't working: [Troubleshooting](troubleshooting.md).
- Remove it all: [Uninstalling](uninstall.md).

## Upgrading

After you upgrade `sb`, run `sb setup` again. It updates the privileged helper, which
keeps its own copy of `sb`, and applies any changes the new version needs. `sb doctor`
tells you when this is due.
