# Uninstalling Switchboard

Removing Switchboard takes three steps: undo the system changes, delete `sb`, and, if
you want, delete your routes and the certificate authority.

**Do step 1 first.** `sb uninstall` is what knows how to undo `sb setup`. Once `sb` is
deleted, nothing reverts those changes (though you can reinstall `sb` just to run
`sb uninstall`).

## 1. Undo the system changes

Run this as your normal user:

```bash
sb uninstall
```

Like `sb setup`, it lists what it will remove, asks `Continue? [y/N]`, and asks for your
password once (one UAC prompt on Windows). It removes:

- the daemon's login service (LaunchAgent, systemd user unit, or Windows Scheduled
  Task), after stopping it;
- the privileged helper's service, its copy of `sb`, its log and its socket (macOS and
  Linux);
- the DNS setting for `.test` (`/etc/resolver/test`, the systemd-resolved or dnsmasq
  drop-in, the `/etc/hosts` block, or the Windows NRPT rule), but only if Switchboard
  wrote it. Files and rules from other tools are left alone;
- Switchboard's certificate authority from the system trust store, and from Firefox's
  and Chrome's stores, as you. Every Switchboard CA of yours is removed, even ones whose
  files you've deleted.

It's safe to run again: anything already gone is skipped. To see the list without
changing anything: `sb uninstall --print-plan`.

If you used the experimental `.local` mode, nothing extra needs undoing: it changes
nothing on the system.

## 2. Delete sb

Use the way you installed it.

**Install script (macOS and Linux):**

```bash
rm -f ~/.local/bin/sb ~/.local/bin/sb.old
```

With `--global`: `sudo rm -f /usr/local/bin/sb /usr/local/bin/sb.old`.
(`sb.old` is the previous version, kept by `sb self-update`.)

**Homebrew:** `brew uninstall sb`, or for the app, `brew uninstall --cask switchboard`.

**Debian, Ubuntu:** `sudo apt remove switchboard`.
**Fedora, RHEL:** `sudo dnf remove switchboard`.

**Windows install script,** in PowerShell:

```powershell
Remove-Item -Recurse -Force "$env:LOCALAPPDATA\Programs\switchboard"
```

Then remove that folder from your user `PATH`: Settings → System → About → Advanced
system settings → Environment Variables. With `-Global`, delete
`%ProgramFiles%\Switchboard` from an elevated PowerShell, and remove it from the
machine `PATH`.

**Scoop:** `scoop uninstall switchboard`. **winget:** `winget uninstall Switchboard.Switchboard`.

**The menu-bar app (macOS):**

1. If you haven't installed the `sb` command, run step 1 with the app's own copy:
   `/Applications/Switchboard.app/Contents/MacOS/sb uninstall`.
2. In the app's menu, turn off **Start at Login**, then **Quit Switchboard**.
3. Move `Switchboard.app` from Applications to the Trash.
4. If you used **Install Command-Line Tool…**, remove the link:
   `sudo rm /usr/local/bin/sb`.
5. Remove the app's own settings: `rm -rf ~/Library/Application\ Support/dev.switchboard.tray`.

## 3. Delete your routes and certificate authority (optional)

`sb uninstall` keeps your routes and the certificate authority's files, so a later
`sb setup` picks up where you left off. To delete them too:

```bash
rm -rf ~/.config/switchboard
```

On Windows: `Remove-Item -Recurse -Force "$env:APPDATA\switchboard"`. If you set
`SWITCHBOARD_CONFIG_DIR` (or `XDG_CONFIG_HOME` on Linux), delete that folder instead.

This also deletes the certificate authority's private key. That's safe after
`sb uninstall`: nothing trusts it any more.

Projects keep their `switchboard.toml` files. They're harmless without Switchboard.

## Check that nothing is left

**macOS:**

```bash
ls /etc/resolver/test /Library/LaunchDaemons/dev.switchboard.helper.plist ~/Library/LaunchAgents/dev.switchboard.daemon.plist
```

```bash
security find-certificate -a -c "Switchboard Local CA" /Library/Keychains/System.keychain
```

Both should find nothing.

**Linux:**

```bash
ls /etc/systemd/resolved.conf.d/switchboard-*.conf /etc/NetworkManager/dnsmasq.d/switchboard-*.conf /etc/systemd/system/switchboard-helper.service ~/.config/systemd/user/switchboard.service
```

```bash
grep -n Switchboard /etc/hosts
```

**Windows,** in PowerShell:

```powershell
Get-DnsClientNrptRule | Where-Object Comment -like '*Switchboard*'
```

```powershell
Get-ChildItem Cert:\LocalMachine\Root | Where-Object Subject -like '*Switchboard Local CA*'
```

Each should print nothing. If something is left, run `sb uninstall` again. If `sb` is
already gone, reinstall it ([Getting started](getting-started.md)), run
`sb uninstall`, and delete it again.

What each file is, and why setup made it: [macOS](setup-macos.md),
[Linux](setup-linux.md), [Windows](setup-windows.md).
