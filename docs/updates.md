# Updates

```bash
sb self-update                  # install the latest stable release, if this install is in its rollout
sb self-update --check          # only say whether there's an update
sb self-update --channel beta   # pre-releases too
sb rollback                     # back to the sb from before the last update (again: forward)
```

The tray app updates itself from **Check for Updates…** in its menu.

## How `sb self-update` works

1. **Is it sb's to update?** It refuses if another tool installed `sb`, and says what to
   run instead. That covers Homebrew (a path under `Cellar`, `/opt/homebrew` or
   Linuxbrew), winget, Scoop, a `.deb` (listed in
   `/var/lib/dpkg/info/switchboard.list`), an `.rpm` (`rpm -qf`), and the `sb` inside
   `Switchboard.app`, which the app updates. Development builds (version `dev`) refuse
   too.
2. **Fetch the manifest** for the channel: `<base>/stable.json` or `<base>/beta.json`.
   The base is `https://nanaaikinson.github.io/switchboard-updates`, or `$SB_UPDATE_URL`.
3. **Rollout.** Each install has a random ID in `<config dir>/install-id`, made on
   first use and never sent anywhere. The bucket is
   `uint32(sha256(id + "\0" + version)[0:4]) % 100`. It's stable for a given install and
   version, and reshuffled for each new version. The update applies only if
   `bucket < rollout_percent`. The tray app computes the same bucket from the same file,
   and a test checks that the Go and Rust code agree.
4. **Download and verify,** in this order, stopping at the first failure:
   1. the archive's SHA-256 must match the manifest;
   2. its **minisign signature** must verify against the release public key built into
      `sb`. Both pre-hashed signatures (minisign 0.10+, Tauri's signer) and legacy ones
      work;
   3. the signature's *trusted comment*, which is itself signed, must be exactly
      `sb <version> <os>-<arch>`, e.g. `sb v0.3.0 darwin-arm64`.

   Step 3 means a compromised manifest host can't replay an old signed release as a
   "new" one, or serve one platform's build to another.
5. **Try it.** `sb` is extracted, written next to the current binary, and run with
   `--version`. It must start and report the promised version.
6. **Swap:** the current binary is renamed to `sb.old` (`sb.old.exe` on Windows), and
   the new one to `sb`. If the second rename fails, the old binary is put back.
7. **Restart the daemon's service:** `launchctl kickstart -k` on macOS,
   `systemctl --user restart` on Linux. The privileged helper keeps its own root-owned
   copy of `sb`. Re-run `sb setup` when a release's notes say the helper changed.

If anything fails before step 6, nothing on disk changes. A build without a release
key (`internal/update.ReleaseKey` empty) refuses to self-update, rather than install
something it can't verify.

## Manifest format

`stable.json` / `beta.json`, for `sb`:

```json
{
  "version": "v0.3.0",
  "notes": "…",
  "pub_date": "2026-10-01T12:00:00Z",
  "rollout_percent": 25,
  "platforms": {
    "darwin-arm64": {
      "url": "https://github.com/nanaaikinson/switchboard/releases/download/v0.3.0/sb_0.3.0_darwin_arm64.tar.gz",
      "sha256": "9f2c…",
      "signature": "dW50cnVzdGVkIGNvbW1lbnQ6…"
    }
  }
}
```

| Field | |
| --- | --- |
| `version` | semver tag. Only a version newer than the running one is installed. |
| `rollout_percent` | 0–100; absent means 100 |
| `platforms` | keyed `GOOS-GOARCH` |
| `url` | the release archive (`.tar.gz`, or `.zip` on Windows); must be `https` |
| `sha256` | lowercase hex |
| `signature` | base64 of the archive's `.minisig` file (the Tauri updater's convention) |

`tray/stable.json` and `tray/beta.json` are the Tauri updater's static format, keyed by
Tauri target (`darwin-aarch64`, `darwin-x86_64`, `windows-x86_64`), plus
`rollout_percent`.

## Release setup (one time)

1. **Make the release key,** offline, with no password so CI can sign without a
   prompt:
   ```bash
   minisign -G -W -p switchboard.pub -s switchboard.key
   ```
   Keep `switchboard.key` out of the repository, and back it up. Losing it means
   installed copies can't verify updates any more.
2. **Repository variables** (public):
   - `SB_UPDATE_PUBLIC_KEY`: the `RW…` line of `switchboard.pub`. It's compiled into
     `sb`.
   - `TAURI_UPDATER_PUBKEY`: `base64 < switchboard.pub` (the whole file). It's put into
     the tray app's updater config.
   - `UPDATE_ROLLOUT_PERCENT` (optional): the rollout for new releases; default 100.
3. **Repository secrets:**
   - `MINISIGN_SECRET_KEY`: the contents of `switchboard.key`.
   - `TAURI_SIGNING_PRIVATE_KEY`: `base64 < switchboard.key`. The same key signs the
     tray app's updater bundles.
   - `UPDATES_DEPLOY_TOKEN`: a fine-grained token with *Contents: write* on the updates
     repository only.
4. **The updates repository:** create a public `switchboard-updates` repository and
   enable GitHub Pages from its default branch root.

After that, every release's `publish-updates` job does the following:
- signs each `sb` archive with the trusted comment above, and uploads the `.minisig`
  files to the release;
- builds `sb.json` and `tray.json` with `go run ./cmd/sb-manifest`, which verifies every
  signature against the public key first;
- commits them to the updates repository: pre-releases to `beta`, and releases to both
  `stable` and `beta`.

Until the variables and secrets exist, the job logs a notice and skips.

**While this repository is private,** release downloads need authentication, so
`sb self-update` can't fetch them. Updates start working once the repository (or
wherever the manifest's URLs point) is public.

### Ramping a rollout

Edit `rollout_percent` in the updates repository, e.g. 10 → 50 → 100, and commit.
Clients pick it up on their next check. To stop a bad release, set it to 0. Installs
that already updated can run `sb rollback`.

## Testing against a local manifest

```bash
SB_UPDATE_URL=https://localhost:8443/updates sb self-update --check
```

The tests (`internal/update`, `cmd/sb/selfupdate_test.go`) cover:
- the bucket math and rollout edges;
- every signature failure: wrong key, forged key ID, altered file, altered trusted
  comment, replayed old version, wrong platform;
- package-manager detection;
- swap and rollback;
- a round trip from `sb-manifest` to the client;
- a real signature made by Tauri's signer (`internal/update/testdata`, with a
  throwaway key whose secret half was deleted).
