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
2. Runs [GoReleaser](https://goreleaser.com) with [.goreleaser.yaml](../.goreleaser.yaml),
   on a macOS runner. It cross-compiles `sb` for darwin, linux and windows on amd64 and
   arm64, with `main.version` set to the tag. A build hook signs and notarizes each
   darwin binary before it's archived (see [below](#macos-signing-and-notarization)).
   It builds:
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
   installs the prebuilt binary on macOS and Linux. If the release was built without
   the macOS signing secrets, the cask removes the download's quarantine flag after
   Homebrew has checked its SHA-256, since Gatekeeper would block the unsigned binary.
   It also pushes the Scoop manifest `switchboard` to
   [nanaaikinson/scoop-bucket](https://github.com/nanaaikinson/scoop-bucket), the
   portable `sb.exe` from the Windows zips. Skipped for pre-releases and while
   `SCOOP_BUCKET_TOKEN` is unset.
4. Signs `SHA256SUMS` with the release key, as `SHA256SUMS.minisig`. Skipped while
   `MINISIGN_SECRET_KEY` is unset.
5. Publishes signed build provenance for every archive and package (GitHub artifact
   attestations). This step is skipped while the repository is private, because GitHub
   does not offer attestations for user-owned private repositories.
6. Then builds the [tray app](tray.md) on macOS (a universal `.dmg`) and Windows (NSIS
   `.exe` and `.msi`), and uploads them to the same release. They're signed when the
   signing secrets below are set, and unsigned otherwise. For a stable release with a
   notarized `.dmg`, the macOS job also pushes the `switchboard` cask (the app) to the
   tap: `brew install --cask nanaaikinson/tap/switchboard`. The Windows job checks the
   installers' signatures with `signtool` before uploading them (when signed), then
   opens a winget PR for `sb` (see [below](#scoop-and-winget)).
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
- doesn't update the Homebrew casks, the Scoop bucket or winget;
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
| `APPLE_CERTIFICATE`, `APPLE_CERTIFICATE_PASSWORD` | secrets | The Developer ID Application certificate, for signing `sb` and the tray app. | [below](#macos-signing-and-notarization) |
| `APPLE_API_KEY_ID`, `APPLE_API_ISSUER_ID`, `APPLE_API_PRIVATE_KEY` | secrets | The App Store Connect API key, for notarizing. | [below](#macos-signing-and-notarization) |
| `WINDOWS_CERTIFICATE`, `WINDOWS_CERTIFICATE_PASSWORD` | secrets | Signing the Windows tray app and installers. Not set up yet. | [below](#windows-signing) |
| `SCOOP_BUCKET_TOKEN` | secret | Pushing the Scoop manifest. A fine-grained token with *Contents: read and write* on `nanaaikinson/scoop-bucket` only. | [below](#scoop-and-winget) |
| `WINGET_TOKEN` | secret | Opening the winget PR. A classic token with the `public_repo` scope. | [below](#scoop-and-winget) |

`GITHUB_TOKEN` is provided by Actions; nothing to set.

**Setting up the tap:** create a public repository `nanaaikinson/homebrew-tap` with a
README, make the token above, and add it as `HOMEBREW_TAP_TOKEN`. The next stable release
writes `Casks/sb.rb`, and `Casks/switchboard.rb` once macOS signing is set up too.
While `switchboard` itself is private, `brew install` can't download the release assets,
so the casks only work once it's public.

## macOS signing and notarization

[macos-sign.sh](../.github/scripts/macos-sign.sh) does the signing, in two jobs that
run on macOS:

- **release:** `setup` makes a temporary keychain with a random password, imports the
  certificate into it, and writes the API key to a temporary file. GoReleaser's build
  hook then signs each darwin `sb` with the hardened runtime and a secure timestamp,
  and notarizes it with `xcrun notarytool`. A bare binary can't hold a stapled ticket,
  so Gatekeeper checks it online.
- **macos:** the same `setup`, then `tauri build` signs the sidecar and the app from
  that keychain, notarizes the app and staples it, and builds the `.dmg`. The job signs,
  notarizes and staples the `.dmg`, checks everything with `codesign`, `stapler` and
  `spctl`, and uploads it.

Both jobs delete the keychain and the key file in a final step that runs even when an
earlier step fails or the run is cancelled. A rejected notarization fails the job and
prints Apple's log. The entitlements are in
[Entitlements.plist](../app/tray/src-tauri/Entitlements.plist) (none are needed).

Set all five secrets, or none: with none, macOS builds are unsigned and the
`switchboard` cask isn't published; with only some, the release fails and names the
missing one. You need a paid [Apple Developer Program](https://developer.apple.com/programs/)
membership, and only its Account Holder can make a Developer ID certificate.

| Secret | Value |
| --- | --- |
| `APPLE_CERTIFICATE` | base64 of the Developer ID Application certificate and private key, as a `.p12` |
| `APPLE_CERTIFICATE_PASSWORD` | the `.p12`'s export password |
| `APPLE_API_KEY_ID` | the API key's Key ID, e.g. `2X9R4HXF34` |
| `APPLE_API_ISSUER_ID` | the Issuer ID, a UUID |
| `APPLE_API_PRIVATE_KEY` | the whole `AuthKey_<KEYID>.p8` file, `-----BEGIN PRIVATE KEY-----` lines included |

The signing identity and team come from the certificate, so there's no secret for them.

**The certificate** (on a Mac, as the Account Holder):

1. In Keychain Access, choose *Keychain Access → Certificate Assistant → Request a
   Certificate From a Certificate Authority*. Enter your email and name, choose *Saved
   to disk*, and save the `.certSigningRequest`.
2. At [developer.apple.com/account](https://developer.apple.com/account), open
   *Certificates, IDs & Profiles → Certificates* and click **+**. Choose
   **Developer ID Application**, then the *G2 Sub-CA* profile type, upload the request,
   and download the `.cer`.
3. Double-click the `.cer` to add it to your login keychain. Under *My Certificates*,
   it shows as `Developer ID Application: <name> (<team ID>)` with its private key
   underneath.
4. Right-click it, **Export…**, choose *Personal Information Exchange (.p12)*, and set a
   strong password. That's `APPLE_CERTIFICATE_PASSWORD`.
5. Base64 it for `APPLE_CERTIFICATE`, then delete the `.p12`:

   ```bash
   base64 -i DeveloperID.p12 | pbcopy
   ```

Developer ID certificates last five years. Keep a backup of the `.p12` somewhere safe,
such as a password manager: Apple limits how many you can make, and apps already signed
keep working after it expires.

**The API key:**

1. At [App Store Connect](https://appstoreconnect.apple.com), open *Users and Access →
   Integrations → App Store Connect API*. The first time, the Account Holder clicks
   *Request Access* and accepts the terms.
2. Under **Team Keys**, click **+** (*Generate API Key*). Name it, for example
   `switchboard-notary`, and give it the **Developer** role, the least that can
   notarize.
3. **Download** the `AuthKey_<KEYID>.p8`. Apple offers it only once. Its contents are
   `APPLE_API_PRIVATE_KEY`.
4. The *Key ID* column gives `APPLE_API_KEY_ID`, and the *Issuer ID* above the table
   gives `APPLE_API_ISSUER_ID`.

To check the key before a release, run this on your Mac. An empty list means it works:

```bash
xcrun notarytool history --key AuthKey_KEYID.p8 --key-id KEYID --issuer ISSUER_ID
```

Revoke the key in the same place if it leaks, and the certificate at
developer.apple.com (revoking a Developer ID certificate also invalidates everything it
signed, so contact Apple first unless the key is compromised).

## Windows signing

Windows releases are **unsigned for now**. Windows SmartScreen warns about the tray
installers ("Windows protected your PC" → *More info* → *Run anyway*). Scoop ignores
Authenticode, and winget accepts unsigned portable packages.

What's in place for when a certificate exists:

- `tauri build` calls [sign-windows.ps1](../app/tray/src-tauri/scripts/sign-windows.ps1)
  for the app, its `sb` sidecar and both installers. It signs with `signtool`, SHA-256
  and an RFC 3161 timestamp (`WINDOWS_TIMESTAMP_URL`, default DigiCert's), from a
  `.pfx` in `WINDOWS_CERTIFICATE` (base64) and `WINDOWS_CERTIFICATE_PASSWORD`.
- Once `WINDOWS_CERTIFICATE` is set, the Windows job runs `signtool verify /pa` on the
  app, the sidecar and each installer before uploading. It fails if any of them isn't
  signed, doesn't chain to a trusted root, or has no timestamp.

A `.pfx` fits a CA certificate you hold yourself. For a cloud-signing service, where
the key never leaves the provider, `sign-windows.ps1` gets a second branch for that
provider's tool. The standalone `sb.exe` in the release zips isn't signed yet either
way: GoReleaser builds it on the macOS runner, where `signtool` doesn't run.

## Scoop and winget

Both install the portable `sb.exe` from the release zips, and both are skipped for
pre-releases.

**Scoop.** GoReleaser writes `bucket/switchboard.json` to
[nanaaikinson/scoop-bucket](https://github.com/nanaaikinson/scoop-bucket). Users run:

```powershell
scoop bucket add switchboard https://github.com/nanaaikinson/scoop-bucket
scoop install switchboard
```

To set it up, create the public repository `nanaaikinson/scoop-bucket` with a README
and an empty `bucket/` directory (a `.gitkeep` inside). Then make a fine-grained token
with *Contents: read and write* on that repository only, and add it as
`SCOOP_BUCKET_TOKEN`. One token covering both the tap and the bucket works too, if you
set it as both secrets.

**winget.** [winget-manifests.sh](../.github/scripts/winget-manifests.sh) writes the
three manifests for `Switchboard.Switchboard` (a `zip` with a `portable` `sb.exe`,
aliased as `sb`, for x64 and arm64), with hashes from `SHA256SUMS`. The Windows job
checks that `wingetcreate.exe` is signed by Microsoft, then runs `wingetcreate submit`.
That forks [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs) into the
token owner's account and opens a PR. winget's validation downloads the zips, so the
step is skipped while this repository is private.

The manifests are written in full every release, instead of with `wingetcreate update`:
the path inside each zip (`sb_<version>_windows_amd64\sb.exe`) changes with every
version, and `update` would keep the old one.

To set it up:

1. On GitHub, open *Settings → Developer settings → Personal access tokens → Tokens
   (classic)*, and generate one with only the **`public_repo`** scope. wingetcreate
   needs a classic token to fork and open a PR on a repository you don't own. Give it
   an expiry and a note like `winget submissions`.
2. Add it as `WINGET_TOKEN`.
3. The first PR adds a new package, so a winget-pkgs moderator reviews it by hand.
   Watch it for review comments. Later versions usually merge automatically once
   validation passes.

The manifests say `License: Proprietary`, since the repository has no LICENSE yet.
Change it in winget-manifests.sh and `.goreleaser.yaml` (the Scoop entry) when one is
added.

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
