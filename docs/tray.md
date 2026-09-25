# Tray app

`app/tray` is Switchboard's menu-bar app, built with [Tauri v2](https://v2.tauri.app).
It ships the `sb` binary inside the app as a Tauri *sidecar*. On macOS it's menu-bar
only (`LSUIElement`): no Dock icon and no app menu.

## What it does

**First launch.** If the daemon isn't reachable and setup hasn't finished before, a
setup wizard opens:

1. It runs `sb setup --print-plan` and shows the exact changes and command, the same
   list `sb setup` prints in a terminal.
2. **Set Up Switchboard** runs `sb setup --yes --admin-dialog`. macOS asks for the
   administrator password in its own dialog (`osascript … with administrator
   privileges`), instead of `sudo` in a terminal the app doesn't have.
3. On success it records that setup finished, in
   `~/Library/Application Support/dev.switchboard.tray/setup-done`, and shows how to add
   a first route.

The wizard refuses to run while macOS is running the app from a temporary location
(App Translocation, or a mounted `.dmg`). Setup points the login services at the
bundled `sb`, so the app must be in its final place first.

**The menu** is rebuilt from `GET /v1/status` whenever `/v1/events` reports a change.
When the daemon isn't reachable, it retries every 3 seconds. The menu has:

| Item | Does |
| --- | --- |
| *N routes* | Header. Says *Paused* while paused, or *Switchboard isn't running* with **Set Up Switchboard…** |
| 🟢 / 🔴 / ⚪ `name :port` | One per route (up / down / not checked yet), with where it comes from. Click to open `https://name` in the browser, or `http://` while HTTPS is down. Pure wildcards (`*.x.test`) aren't clickable. After 25 routes: *…and N more*, which opens the dashboard. |
| **Add Route…** | A small window: name, port, HTTPS ("Serve this over TLS"). It calls `POST /v1/routes`. |
| **Open Dashboard** | Gets a one-time sign-in token from the control socket and opens the dashboard in an app window. Disabled while HTTPS is down. |
| **Pause All** | Check item. `POST /v1/pause`: every route answers 503 until unchecked (or `sb resume`, or a daemon restart). |
| **Install Command-Line Tool…** | Links `/usr/local/bin/sb` to the app's bundled `sb` (admin dialog), so the command updates with the app. Asks before replacing an existing `sb`, e.g. one from Homebrew or `install.sh`. |
| **Check for Updates…** | Checks the signed Tauri updater manifest (`tray/stable.json` on the update host; see [updates.md](updates.md)). It applies the same staged rollout as `sb self-update`, then offers to install and restart. In builds without an updater key, it only compares with the latest GitHub release and opens its page. |
| **Start at Login** | Check item. Adds or removes a LaunchAgent for the *app* (tauri-plugin-autostart). The daemon already starts at login on its own, via `sb setup`. |
| **Quit Switchboard** | Quits the app. The daemon keeps running. |

## Security

- **The app talks to the daemon only through the control socket** (`~/.config/switchboard/sb.sock`,
  mode 0600), as the CLI does. It never touches system files itself. Privileged steps
  go through `sb`, behind the OS password dialog.
- **Only the app's own pages can call its commands.** Those are the wizard and Add
  Route (`capabilities/local-windows.json`). The dashboard window loads
  `https://switchboard.<tld>`, a remote page, and has no IPC access.
- **The local pages have a strict CSP:** `script-src 'self'`, no remote content.
- **Admin commands are quoted twice,** for `/bin/sh` and then as an AppleScript string.
  Tests round-trip hostile paths through a real shell.
- **No credentials are in the repository.** Signing reads only from environment
  variables (below).

## Building

```bash
make tray        # npm ci && tauri build --bundles app → an unsigned Switchboard.app
make test-tray   # sidecar, cargo fmt --check, clippy -D warnings, cargo test
```

It needs Go, Node and Rust. [app/tray/rust-toolchain.toml](../app/tray/rust-toolchain.toml)
pins stable Rust for that directory only, so your global default is left alone.
`scripts/build-sidecar.sh` builds `sb` into `src-tauri/binaries/sb-<target triple>`
before every `tauri build`, including universal macOS builds, which it merges with
`lipo`.

Bundles are set per platform: `tauri.macos.conf.json` builds `.app` and `.dmg`, and
`tauri.windows.conf.json` builds NSIS (`.exe`) and MSI installers. The release
workflow builds a universal `.dmg` and the Windows installers for each tag and attaches
them to the GitHub release. See [releasing.md](releasing.md).

### Signing

Set these as repository secrets (for CI) or environment variables (locally). Without
them, builds are unsigned.

| Variable | For |
| --- | --- |
| `APPLE_CERTIFICATE` | base64 of the Developer ID Application `.p12` |
| `APPLE_CERTIFICATE_PASSWORD` | its password |
| `APPLE_SIGNING_IDENTITY` | e.g. `Developer ID Application: Name (TEAMID)` |
| `APPLE_ID`, `APPLE_PASSWORD`, `APPLE_TEAM_ID` | notarization; the password is an app-specific password |
| `WINDOWS_CERTIFICATE` | base64 of the code-signing `.pfx` ([sign-windows.ps1](../app/tray/src-tauri/scripts/sign-windows.ps1)) |
| `WINDOWS_CERTIFICATE_PASSWORD` | its password |
| `WINDOWS_TIMESTAMP_URL` | optional; default `http://timestamp.digicert.com` |

Tauri signs the bundled `sb` sidecar too, with the hardened runtime, as notarization
requires.

## Manual QA on macOS

Use a macOS 13+ VM with a snapshot, as in [setup-macos.md](setup-macos.md). Don't run
this on your main machine: setup changes system files.

**Install**
1. Build a `.dmg`: `cd app/tray && npx tauri build --bundles dmg`. Copy it to the VM
   and open it. The window shows the app and an Applications shortcut.
2. Open the app **straight from the mounted `.dmg`**. The menu-bar icon appears (a
   black template icon that turns white in a dark menu bar), with no Dock icon. The
   wizard opens, then says to move the app to Applications first, with no Set Up
   button. Quit from the menu.
3. Drag the app to Applications and open it. The unsigned build needs right-click →
   Open once. The wizard lists the same 6 changes as `sb setup`.

**Setup**
4. **Not Now** closes the wizard. The menu says *Switchboard isn't running*.
   **Set Up Switchboard…** reopens it.
5. **Set Up Switchboard**: macOS shows its password dialog, with the text "Switchboard
   wants to set up .test names…". Cancel it: the wizard says setup didn't finish and
   that nothing changed. Check the baseline from setup-macos.md step 2: nothing is
   installed.
6. **Try Again** and enter the password. macOS may ask a second time, to change the
   System keychain's trust settings. The wizard shows *Switchboard is ready*, and
   within a few seconds the menu says *No routes yet*.
7. Check the result like setup-macos.md steps 6 to 12. The LaunchAgent must point at
   `/Applications/Switchboard.app/Contents/MacOS/sb`. `sb doctor` passes (run it as
   `/Applications/Switchboard.app/Contents/MacOS/sb doctor` until step 12).
8. Quit and reopen the app. No wizard.

**Menu**
9. `python3 -m http.server 3000`, then **Add Route…** with name `myapp`, port `3000`.
   Within a second the menu shows 🟢 `myapp.test :3000`. Invalid input (`My App`, port
   `70000`) shows errors in the window. `sb add` for an existing name updates it.
10. Click the route: Safari opens https://myapp.test with no certificate warning. Stop
    the server: the dot turns 🔴 within about 5 seconds, without reopening the menu.
    `sb add api 4000` in a terminal appears in the menu at once, and a Docker container
    with a published port appears as `(docker: name)`.
11. **Open Dashboard**: a Switchboard window shows the dashboard, already signed in.
    Close it; the app keeps running. Open it again: a new sign-in, and it works. With
    port 443 taken (`sudo nc -l 443`, then restart the daemon), the item is disabled
    and route clicks open `http://`.
12. **Install Command-Line Tool…**: password dialog, then "Installed". A new terminal
    runs `sb --help`, and `readlink /usr/local/bin/sb` points into the app. Run it
    again: "already installed". With another file at `/usr/local/bin/sb`, it asks
    before replacing it; Cancel leaves the file alone.
13. **Pause All**: the item gets a checkmark, and https://myapp.test shows "Switchboard
    is paused" (503). `sb ls` warns. **Pause All** again, or `sb resume`, restores it,
    and the checkmark follows `sb pause` and `sb resume` from a terminal.
14. **Start at Login**: checked → `~/Library/LaunchAgents/Switchboard.plist` exists.
    Log out and in: the app is in the menu bar. Uncheck it: the plist is gone.
15. **Check for Updates…**: with no newer release, "You're up to date". Offline, or
    while the repository is private, it shows a "Couldn't check" error, not a hang. In
    a build with the updater key, and a manifest with a newer version and
    `rollout_percent` 100, it offers Install and Restart. After installing, the app
    relaunches at the new version. With `rollout_percent` 0, it says the update isn't
    offered to this Mac yet.
16. Dark mode (System Settings → Appearance): the wizard and Add Route follow it, and
    the menu-bar icon stays legible.

**Removal**
17. Stop the daemon (`launchctl bootout gui/$(id -u)/dev.switchboard.daemon`). Within 3
    seconds the menu says *isn't running*. Start it again
    (`launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.switchboard.daemon.plist`):
    routes come back, without restarting the app.
18. `sb uninstall`, then check setup-macos.md step 23: nothing is left. Uncheck Start at
    Login, quit the app, delete it from Applications, remove `/usr/local/bin/sb` if you
    installed it, and remove `~/Library/Application Support/dev.switchboard.tray`.

**Signed builds only**
19. With the signing secrets set, the release `.dmg` opens with no Gatekeeper warning.
    `spctl -a -vv /Applications/Switchboard.app` says "accepted, source=Notarized
    Developer ID", and `codesign -dv --verbose=2 …/Contents/MacOS/sb` shows the same
    team and the hardened runtime.
