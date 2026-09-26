# Releasing

Every push to `main` can cut a release. The [auto-tag workflow](../.github/workflows/autotag.yml):

1. Runs the full CI workflow (build, test, lint on macOS and Ubuntu, shellcheck, the
   Linux end-to-end test).
2. Works out the next version from the [conventional commit](https://www.conventionalcommits.org)
   subjects since the last stable tag (`vX.Y.Z`; pre-release tags are ignored), with
   [next-version.sh](../.github/scripts/next-version.sh):

   | Commits since the last release | Next version |
   | --- | --- |
   | any `feat:` | minor: `v0.3.1` → `v0.4.0` |
   | `fix:` or `perf:`, no `feat:` | patch: `v0.3.1` → `v0.3.2` |
   | `feat!:` / `fix!:`, or a `BREAKING CHANGE:` footer | minor before 1.0, major from 1.0 |
   | only `docs:`, `chore:`, `ci:`, `test:`, `refactor:`, `style:`, `build:` | no release |

3. Pushes that tag, then calls the [release workflow](../.github/workflows/release.yml)
   for it directly. A tag pushed with the workflow's own `GITHUB_TOKEN` can't trigger
   other workflows, so no personal access token is needed.

Pushes run one at a time, in order, so two quick merges can't claim the same tag. With
squash merges, the PR title is the commit subject that counts.

The release workflow:

1. Rejects the tag unless it matches `vMAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]`.
2. Runs [GoReleaser](https://goreleaser.com) with [.goreleaser.yaml](../.goreleaser.yaml).
   It cross-compiles `sb` for darwin, linux and windows on amd64 and arm64, with
   `main.version` set to the tag. It builds:
   - `sb_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows), each holding
     `sb_<version>_<os>_<arch>/sb`;
   - `.deb` and `.rpm` packages named `switchboard`, which install `/usr/bin/sb`;
   - `SHA256SUMS` over all of them, which [install.sh](../install/install.sh) checks.

   It then creates the GitHub release. Its notes list the commits since the previous
   tag, grouped as *Breaking changes*, *Features*, *Fixes*, *Performance* and *Other*.
   `docs:`, `chore:`, `ci:`, `test:`, `refactor:`, `style:` and `build:` commits are left
   out, matching what auto-tag counts. Tags with a pre-release part (`v0.2.0-beta.1`,
   `v0.2.0-rc.1`) are marked as pre-releases.
3. Pushes the Homebrew cask `sb` to
   [nanaaikinson/homebrew-tap](https://github.com/nanaaikinson/homebrew-tap), so
   `brew install nanaaikinson/tap/sb` and `brew upgrade sb` get the release. Pre-releases
   never update the cask, and it's skipped while `HOMEBREW_TAP_TOKEN` is unset. The cask
   installs the prebuilt binary on macOS and Linux. Until the binary is notarized, the
   cask removes the download's quarantine flag after Homebrew has checked its SHA-256.
4. Signs `SHA256SUMS` with the release key, as `SHA256SUMS.minisig`. Skipped while
   `MINISIGN_SECRET_KEY` is unset.
5. Publishes signed build provenance for every archive and package (GitHub artifact
   attestations). This step is skipped while the repository is private, because GitHub
   does not offer attestations for user-owned private repositories.
6. Then builds the [tray app](tray.md) on macOS (a universal `.dmg`) and Windows (NSIS
   `.exe` and `.msi`), and uploads them to the same release. They're signed when the
   signing secrets listed in tray.md are set, and unsigned otherwise.
7. Signs each `sb` archive with the release key, and publishes the update manifests
   that `sb self-update` and the tray app read (see [updates.md](updates.md)). This is
   skipped until the key and deploy token are configured.

Releases cut by auto-tag get the same secrets as tag pushes: autotag.yml passes them on
to the release workflow (`secrets: inherit`).

## Pre-releases (beta)

Push a tag with a pre-release part by hand, for example `v0.3.0-beta.1`. Auto-tag never
makes these. A pre-release:

- is marked as a pre-release on GitHub, so "latest" still points at the last stable
  release, and `install.sh` only installs it when asked with `SB_VERSION`;
- doesn't update the Homebrew cask;
- goes to the `beta` update channel only (see [updates.md](updates.md)).

## Secrets and variables

Set these under *Settings → Secrets and variables → Actions*. Never commit their values.
Each feature that needs one is skipped with a notice until it is set, so a release still
works without any of them.

| Name | Kind | Used for | Where it's described |
| --- | --- | --- | --- |
| `HOMEBREW_TAP_TOKEN` | secret | Pushing the cask. A fine-grained token with *Contents: read and write* on `nanaaikinson/homebrew-tap` only. | here |
| `MINISIGN_SECRET_KEY` | secret | Signing `SHA256SUMS` and each `sb` archive. The contents of the minisign secret key file, made with `minisign -G -W`. | [updates.md](updates.md) |
| `SB_UPDATE_PUBLIC_KEY` | variable | The matching public key, built into `sb` for `sb self-update`. | [updates.md](updates.md) |
| `UPDATES_DEPLOY_TOKEN` | secret | Publishing update manifests to `switchboard-updates`. | [updates.md](updates.md) |
| `UPDATE_ROLLOUT_PERCENT` | variable | Optional staged rollout of updates. | [updates.md](updates.md) |
| `TAURI_SIGNING_PRIVATE_KEY`, `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | secrets | Signing the tray app's updater bundles. | [updates.md](updates.md) |
| `TAURI_UPDATER_PUBKEY` | variable | The tray app updater's public key. | [updates.md](updates.md) |
| `APPLE_CERTIFICATE`, `APPLE_CERTIFICATE_PASSWORD`, `APPLE_SIGNING_IDENTITY`, `APPLE_ID`, `APPLE_PASSWORD`, `APPLE_TEAM_ID` | secrets | Signing and notarizing the macOS tray app. | [tray.md](tray.md) |
| `WINDOWS_CERTIFICATE`, `WINDOWS_CERTIFICATE_PASSWORD` | secrets | Signing the Windows installers. | [tray.md](tray.md) |

`GITHUB_TOKEN` is provided by Actions; nothing to set.

**Setting up the tap:** create a public repository `nanaaikinson/homebrew-tap` with a
README, make the token above, and add it as `HOMEBREW_TAP_TOKEN`. The next stable release
writes `Casks/sb.rb`. While `switchboard` itself is private, `brew install` can't
download the release assets, so the cask only works once it's public.

## Linux packages and systemd

The `.deb` and `.rpm` install `/usr/bin/sb` and nothing else; their scripts only print
what to do next. The systemd units (the root helper, and the daemon's user service) are
written by `sb setup` for the user who runs it, and removed by `sb uninstall`. Shipping
them in the package as well would give two owners to the same files.

## Releasing by hand

Pre-releases, or a release the commits wouldn't produce, still come from pushing a tag
yourself. The release workflow runs CI first:

```bash
git tag -a v0.2.0-rc.1 -m "v0.2.0-rc.1"
git push origin v0.2.0-rc.1
```

To skip releasing a push to `main`, use only non-releasing commit types, like
`chore:`, or merge with such a PR title.

Build the same artifacts locally, without publishing, with `make dist`. It needs
GoReleaser v2 installed. `goreleaser check` validates the config; CI runs it on every
push. `make lint-sh` also runs the version script's tests.

Verify a downloaded archive:

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
minisign -Vm SHA256SUMS -P <release public key>
gh attestation verify sb_0.1.0_darwin_arm64.tar.gz --repo nanaaikinson/switchboard
```

`gh attestation verify` works only for releases built while the repository was public.

## Versioning rules

- Before 1.0, a minor bump (`v0.2.0`) may change the config format or control API. Each
  config change ships a migration (see [config.md](config.md)).
- Patch bumps (`v0.1.1`) are fixes only.
- From 1.0, the config and control API are frozen. Breaking changes need a major bump.
- Never move or delete a published tag. Fix mistakes by releasing a new patch.
