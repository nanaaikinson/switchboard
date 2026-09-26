# Installing sb

## Homebrew (macOS and Linux)

```bash
brew install nanaaikinson/tap/sb
```

This installs the prebuilt `sb` from the latest stable release. Then run `sb setup`.
Pre-releases are never published to the tap. Update with `brew upgrade sb`.

For the menu-bar app instead, which bundles `sb` and links it the same way:

```bash
brew install --cask nanaaikinson/tap/switchboard
```

Open it once to run setup. It updates itself (**Check for Updates…**). The two casks
conflict, since both provide `sb`; install one or the other. Run `sb uninstall` before
`brew uninstall` to undo setup's system changes.

## macOS and Linux

```bash
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.sh | sh
```

`--proto '=https' --tlsv1.2` keeps curl on HTTPS with TLS 1.2 or later, redirects
included, so the script itself can't be fetched over plain HTTP.

[install.sh](../install/install.sh) is POSIX `sh`. It:

1. Detects the OS (`darwin` or `linux`) and CPU (`amd64` or `arm64`). An x86_64 shell
   running under Rosetta on Apple silicon still gets the `arm64` build.
2. Downloads `sb_<version>_<os>_<arch>.tar.gz` and `SHA256SUMS` from the GitHub release.
   With `curl` (preferred), every request and redirect must be HTTPS. `wget` has no
   such option, so with only `wget` a redirect to plain HTTP would be followed; the
   checks below still apply to what it downloads.
3. Checks `SHA256SUMS` against `SHA256SUMS.minisig` with the release public key built
   into the script, using `minisign`. The signature's trusted comment must be exactly
   `switchboard <version> SHA256SUMS`, so another release's signature is refused too.
   It refuses to install if the signature is missing or invalid, and if `minisign`
   isn't installed (the error says how to get it). `SB_INSECURE_SKIP_SIGNATURE=1`
   installs without `minisign`, with a warning; it never skips a signature that
   `minisign` found invalid.

   **The release key doesn't exist yet,** so the script has none, and this step only
   prints a warning that the release is not signature-verified. Until then only the
   checksum (step 4) protects the download, which catches corruption but not a
   tampered release.
4. Checks the archive's SHA-256 against `SHA256SUMS`, and refuses to install on a
   mismatch or if the archive isn't listed.
5. Installs `sb` to `~/.local/bin`. It copies to a temp file next to the target, then
   renames it into place. With `--global` it installs to `/usr/local/bin` instead, using
   `sudo` only if that directory isn't writable.
6. Warns if the install directory isn't on your `PATH`, prints the line to add, and
   tells you to run `sb setup`.

Options:

| | |
| --- | --- |
| `--global` | install to `/usr/local/bin` |
| `SB_VERSION=v0.1.0` | install that release instead of the latest; the `v` is optional |
| `SB_INSECURE_SKIP_SIGNATURE=1` | install without `minisign`, so without checking the release signature (not recommended) |

```bash
curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.sh | SB_VERSION=v0.1.0 sh -s -- --global
```

`latest` means the newest release that isn't a pre-release. To install a pre-release
such as `v0.2.0-rc.1`, set `SB_VERSION`.

The script only installs the binary. `sb setup` makes the system changes (see
[setup-macos.md](setup-macos.md) and [setup-linux.md](setup-linux.md)). Re-run `sb setup` after every upgrade so that the
root helper's copy of `sb` is refreshed.

## Debian, Ubuntu, Fedora and other Linux distros

Every release also has `.deb` and `.rpm` packages that install `/usr/bin/sb`:

```bash
sudo apt install ./switchboard_0.2.0_amd64.deb
```

```bash
sudo dnf install ./switchboard-0.2.0-1.x86_64.rpm
```

Then run `sb setup` as your normal user. See [setup-linux.md](setup-linux.md). The
packages recommend `libnss3-tools` / `nss-tools`, which Chrome and Firefox trust
needs. Before you remove the package, run `sb uninstall`, because the package can't
undo a per-user setup.

## Windows

With [Scoop](https://scoop.sh) or winget:

```powershell
scoop bucket add switchboard https://github.com/nanaaikinson/scoop-bucket
scoop install switchboard
```

```powershell
winget install Switchboard.Switchboard
```

Both install the portable `sb.exe` from the latest stable release, and update with
`scoop update switchboard` or `winget upgrade Switchboard.Switchboard`. Run
`sb uninstall` before `scoop uninstall` or `winget uninstall` to undo setup's system
changes; once `sb.exe` is gone, nothing can.

Or with the install script, in PowerShell (Windows PowerShell 5.1 or pwsh 7), as your
normal user:

```powershell
irm https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.ps1 | iex
```

[install.ps1](../install/install.ps1) does the same steps as install.sh:

1. Detects the CPU (`amd64` or `arm64`), including from a 32-bit PowerShell.
2. Downloads `sb_<version>_windows_<arch>.zip` and `SHA256SUMS` over HTTPS (TLS 1.2
   forced on 5.1).
3. Checks `SHA256SUMS.minisig` with `minisign` and the release key, as install.sh does:
   the trusted comment must name this release, and a missing `minisign` is an error
   unless `SB_INSECURE_SKIP_SIGNATURE=1`. Until the release key exists, it warns that
   the release is not signature-verified instead.
4. Checks the zip's SHA-256 with `Get-FileHash`, refusing on a mismatch or if it isn't
   listed.
5. Installs `sb.exe` to `%LOCALAPPDATA%\Programs\switchboard`. A running `sb.exe` is
   renamed to `sb.old.exe` first (Windows can't overwrite a running binary, and
   `sb rollback` uses that name too).
6. Adds the folder to your user `PATH`, and to the current session, then tells you to
   run `sb setup`.

Options, as parameters when you run the file, or environment variables through `iex`:

| Parameter | Variable | |
| --- | --- | --- |
| `-Global` | `SB_GLOBAL=1` | install to `%ProgramFiles%\Switchboard` and the machine `PATH`; needs an elevated PowerShell |
| `-NoModifyPath` | `SB_NO_MODIFY_PATH=1` | don't touch `PATH`; print how to add it |
| | `SB_VERSION=v0.1.0` | install that release instead of the latest |
| | `SB_INSECURE_SKIP_SIGNATURE=1` | install without `minisign`, so without checking the release signature (not recommended) |

```powershell
$env:SB_VERSION = 'v0.1.0'; irm https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.ps1 | iex
```

An error doesn't close the window when run through `iex`. Then run `sb setup`; see
[setup-windows.md](setup-windows.md).

## Updating

`sb self-update` installs the latest release in place and keeps the previous one as
`sb.old`; `sb rollback` goes back. See [updates.md](updates.md). Installs from a
package manager update with that package manager instead (`brew upgrade sb`, `apt`,
`dnf`), and `sb self-update` says so.

## Uninstalling

Run `sb uninstall` to revert `sb setup`. Then delete the binary: `rm ~/.local/bin/sb`,
or `sudo rm /usr/local/bin/sb` if you used `--global`, or remove the `switchboard`
package. On Windows, delete `%LOCALAPPDATA%\Programs\switchboard` and remove it from
your user `PATH` (Settings → System → About → Advanced system settings → Environment
Variables).

## Enabling signature checks (maintainers)

1. Generate a key pair offline with `minisign -G`. Keep the secret key out of the repo.
2. Store it as the `MINISIGN_SECRET_KEY` secret. The release workflow then signs
   `SHA256SUMS` as `SHA256SUMS.minisig` ([releasing.md](releasing.md)).
3. Put the public key (the `RW...` line, the same as `SB_UPDATE_PUBLIC_KEY`) in
   `MINISIGN_PUBKEY` in `install/install.sh` and `$SbMinisignPubkey` in
   `install/install.ps1`. Both are empty so far.

After step 3, the scripts fail closed: they refuse a release without a valid signature
whose trusted comment names it, and refuse to run without `minisign` unless
`SB_INSECURE_SKIP_SIGNATURE=1`. So finish step 2 and cut one signed release before
merging step 3. `test/install/install-test.sh` and `install-test.ps1` test both states.
