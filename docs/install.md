# Installing sb

## macOS and Linux

```bash
curl -fsSL https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.sh | sh
```

[install.sh](../install/install.sh) is POSIX `sh`. It:

1. Detects the OS (`darwin` or `linux`) and CPU (`amd64` or `arm64`). An x86_64 shell
   running under Rosetta on Apple silicon still gets the `arm64` build.
2. Downloads `sb_<version>_<os>_<arch>.tar.gz` and `SHA256SUMS` from the GitHub release.
   It uses `curl` or `wget`, HTTPS only.
3. If `minisign` is installed and the script has a release public key, checks
   `SHA256SUMS` against `SHA256SUMS.minisig`, and refuses to install if the signature is
   missing or invalid. Releases aren't signed yet, so for now this step is skipped with
   a message.
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

```bash
curl -fsSL https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.sh | SB_VERSION=v0.1.0 sh -s -- --global
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

Download the `.zip` for your CPU from the
[releases page](https://github.com/nanaaikinson/switchboard/releases).

## Updating

`sb self-update` installs the latest release in place and keeps the previous one as
`sb.old`; `sb rollback` goes back. See [updates.md](updates.md). Installs from a
package manager update with that package manager instead (`brew upgrade`, `apt`,
`dnf`), and `sb self-update` says so.

## Uninstalling

Run `sb uninstall` to revert `sb setup`. Then delete the binary: `rm ~/.local/bin/sb`,
or `sudo rm /usr/local/bin/sb` if you used `--global`, or remove the `switchboard`
package.

## Enabling signature checks (maintainers)

1. Generate a key pair offline with `minisign -G`. Keep the secret key out of the repo.
2. Store it as a release-workflow secret, and have the release job sign `SHA256SUMS` to
   produce `SHA256SUMS.minisig`.
3. Put the public key (the `RW...` line) in `MINISIGN_PUBKEY` in `install/install.sh`.

After step 3, installs with `minisign` present fail closed on releases without a valid
signature. So finish step 2 and cut one signed release before merging step 3.
