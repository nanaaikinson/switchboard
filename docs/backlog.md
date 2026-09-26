# Backlog: signing and releasing

Work that was postponed. Each part of the release pipeline skips with a notice until its
secrets exist, so releases keep working in the meantime. Setup steps for everything
below are in [releasing.md](releasing.md).

## Before going public

- [x] **Add a LICENSE:** MIT, in [LICENSE](../LICENSE) and the winget, Scoop and
  `.deb`/`.rpm` metadata.
- [x] **Make the repository public.** Attestations, Homebrew and Scoop downloads and the
  winget step work from the next release; the project can also apply for free signing
  from SignPath Foundation.
- [ ] **Confirm the winget identity before the first PR.** The package identifier
  `Switchboard.Switchboard` (from the product spec) and the `Publisher` field are hard
  to change once published.

## macOS signing

The pipeline is built ([releasing.md](releasing.md#macos-signing-and-notarization)).
Builds are unsigned until the secrets exist.

- [ ] Join the Apple Developer Program. The Account Holder then creates the Developer
  ID Application certificate and the App Store Connect API key.
- [ ] Set `APPLE_CERTIFICATE`, `APPLE_CERTIFICATE_PASSWORD`, `APPLE_API_KEY_ID`,
  `APPLE_API_ISSUER_ID` and `APPLE_API_PRIVATE_KEY`.
- [ ] Delete the old secrets `APPLE_SIGNING_IDENTITY`, `APPLE_ID`, `APPLE_PASSWORD` and
  `APPLE_TEAM_ID`, if they were ever set. Nothing reads them any more.
- [ ] After the first signed release:
  - run [tray.md](tray.md) QA step 19 on a clean VM;
  - check the `sb` cask no longer strips quarantine (its postflight should be empty);
  - check the `switchboard` cask installs;
  - check `xcrun stapler validate` on the downloaded `.dmg`.
- [ ] Check whether Tauri builds the updater `.app.tar.gz` before or after it staples
  the app. If before, rebuild and re-sign it from the stapled app. This is low
  priority, because updater installs aren't quarantined.
- [ ] Decide whether to publish the `switchboard` (app) cask while builds are unsigned,
  with the same quarantine removal the `sb` cask uses. For now it's published only for
  notarized builds.
- [ ] While macOS signing is off, the release job runs on a macOS runner for nothing,
  which costs about 10× the minutes of Ubuntu on a private repository. Optionally switch
  it back to `ubuntu-latest` until the Apple secrets exist.

## Windows signing

Releases are unsigned ([releasing.md](releasing.md#windows-signing)).

- [ ] **Choose a provider.** None is free for this repository today:
  - SignPath Foundation: free, but needs a public repository with an OSI licence, and
    the certificate names "SignPath Foundation".
  - Certum Open Source: cheapest CA certificate, but its cloud signing is awkward in CI.
  - SSL.com eSigner or DigiCert KeyLocker: CA certificate with cloud signing, available
    worldwide.
  - Azure Artifact Signing: individual developers only in the US or Canada.
- [ ] Add that provider's branch to
  [sign-windows.ps1](../app/tray/src-tauri/scripts/sign-windows.ps1), keeping the
  `.pfx` branch, with the provider's credentials as secrets. Keep the timestamp.
- [ ] **Sign the standalone `sb.exe`** in the release zips. It's built by GoReleaser on
  the macOS runner, so it needs a cross-platform signer in a build hook: jsign, which
  also supports the cloud providers, or osslsigncode for a `.pfx`. Also verify it
  before it's archived. Only then do Scoop and winget install a signed `sb.exe`.
- [ ] After the first signed release, check that the `signtool verify` step finds the
  app at `target/x86_64-pc-windows-msvc/release/switchboard-tray.exe` and the sidecar at
  `…/release/sb.exe`. Both paths are assumed, not yet seen on a signed build.

## Package managers

- [ ] Create `nanaaikinson/homebrew-tap` and `nanaaikinson/scoop-bucket` (the bucket
  with an empty `bucket/`), and set `HOMEBREW_TAP_TOKEN` and `SCOOP_BUCKET_TOKEN`.
- [ ] Set `WINGET_TOKEN` (a classic token with `public_repo`), then follow the first
  winget PR through moderator review.
- [ ] The tray app isn't in winget or Scoop, because both ship only the CLI for now.
  Revisit once the Windows installers are signed.

## Update and checksum signing

- [x] Generate the minisign release key and pin its public key in
  [install.sh](../install/install.sh), [install.ps1](../install/install.ps1) and
  `internal/update/key.go` ([install.md](install.md#enabling-signature-checks-maintainers)).
- [x] Set the `MINISIGN_SECRET_KEY` secret and the `SB_UPDATE_PUBLIC_KEY` variable
  ([updates.md](updates.md)).
- [ ] Set `UPDATES_DEPLOY_TOKEN` and create the `switchboard-updates` Pages repository.
- [x] Set `TAURI_SIGNING_PRIVATE_KEY` and `TAURI_UPDATER_PUBKEY` from the same
  release key. It has no password, so `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` stays unset.
- [ ] Cut the first signed release and check that the Tauri signer accepts a
  `minisign -W` key and that the install scripts install it.
