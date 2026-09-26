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
   Every request, and every redirect, must be `https`; plain `http` is allowed only to
   a loopback host (`localhost`, `127.0.0.1`, `::1`) for a local test server. An
   `SB_UPDATE_URL` that isn't is an error.
3. **Verify the manifest** before reading anything in it. `<channel>.json.minisig` must
   be a valid signature of the exact bytes fetched, made with the release key built
   into `sb`, and its trusted comment must be exactly
   `sb-manifest <channel> <pub_date>`, e.g. `sb-manifest stable 2026-10-01T12:00:00Z`,
   with the manifest's own `pub_date`. Then:
   - `pub_date` may not be more than an hour ahead of this computer's clock;
   - it may not be older than the newest `pub_date` this install has accepted on that
     channel, which is kept in `<config dir>/update-state.json` (mode 0600). Equal is
     fine: that's the same manifest again;
   - the stable channel may not offer a pre-release.

   So whoever controls the update host can't change the rollout, the version or the
   URLs, move a beta manifest to stable, or serve an older signed manifest to hold an
   install back once it has seen a newer one.
4. **Rollout.** Each install has a random ID in `<config dir>/install-id`, made on
   first use and never sent anywhere. The bucket is
   `uint32(sha256(id + "\0" + version)[0:4]) % 100`. It's stable for a given install and
   version, and reshuffled for each new version. The update applies only if
   `bucket < rollout_percent`. The tray app computes the same bucket from the same file,
   and a test checks that the Go and Rust code agree.
5. **Download and verify,** in this order, stopping at the first failure:
   1. the archive's SHA-256 must match the manifest;
   2. its **minisign signature** must verify against the release public key built into
      `sb`. Both pre-hashed signatures (minisign 0.10+, Tauri's signer) and legacy ones
      work;
   3. the signature's *trusted comment*, which is itself signed, must be exactly
      `sb <version> <os>-<arch>`, e.g. `sb v0.3.0 darwin-arm64`.

   The trusted comment means a compromised manifest host can't replay an old signed
   release as a "new" one, or serve one platform's build to another.
6. **Try it.** `sb` is extracted, written next to the current binary, and run with
   `--version`. It must start and report the promised version.
7. **Swap:** the current binary is renamed to `sb.old` (`sb.old.exe` on Windows), and
   the new one to `sb`. If the second rename fails, the old binary is put back.
8. **Restart the daemon's service:** `launchctl kickstart -k` on macOS,
   `systemctl --user restart` on Linux, and stopping and starting the logon task on
   Windows. The privileged helper keeps its own root-owned
   copy of `sb`. Re-run `sb setup` when a release's notes say the helper changed.

If anything fails before step 7, nothing on disk changes except `update-state.json`. A
build without a release key (`internal/update.ReleaseKey` empty) refuses to
self-update, rather than install something it can't verify.

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
| `version` | semver tag. Only a version newer than the running one is installed; never a pre-release on `stable` |
| `pub_date` | RFC 3339, required; signed in the trusted comment of `<channel>.json.minisig` |
| `rollout_percent` | 0–100; absent means 100 |
| `platforms` | keyed `GOOS-GOARCH` |
| `url` | the release archive (`.tar.gz`, or `.zip` on Windows); must be `https` |
| `sha256` | lowercase hex |
| `signature` | base64 of the archive's `.minisig` file (the Tauri updater's convention) |

Next to each manifest is its signature, `stable.json.minisig` / `beta.json.minisig`, a
plain minisign signature file with the trusted comment `sb-manifest <channel> <pub_date>`.
The same `sb.json` is published to both channels, each copy signed for its own channel.

`tray/stable.json` and `tray/beta.json` are the Tauri updater's static format, keyed by
Tauri target (`darwin-aarch64`, `darwin-x86_64`, `windows-x86_64`), plus
`rollout_percent`.

Tauri checks each bundle's signature, but takes the version to install from the
manifest, and Tauri's bundle signatures don't name a version. So each tray manifest is
signed too, as `tray/<channel>.json.minisig`, with the trusted comment
`switchboard-tray <channel> <version>`, e.g. `switchboard-tray stable 0.3.0`. Before
**Check for Updates…** installs anything, the tray app:

1. fetches the manifest from its configured endpoint, and its `.minisig`, over HTTPS
   only (redirects included);
2. verifies the signature over those exact bytes with the updater public key built into
   the app (`TAURI_UPDATER_PUBKEY`, the same release key), and requires the trusted
   comment to name the endpoint's channel and the manifest's `version`. The stable
   channel may not offer a pre-release;
3. has Tauri check that same endpoint, and refuses unless Tauri read the same manifest
   and is about to install exactly the signed version.

So an old, validly signed bundle can't be offered as a newer version. The rollout
percentage comes from the verified manifest too.

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
  signature against the public key first (so the Tauri key must be the release key);
- copies them to each channel of the updates repository (pre-releases to `beta`, and
  releases to both `stable` and `beta`), signs each `<channel>.json` as
  `sb-manifest <channel> <pub_date>`, and checks the result with
  `sb-manifest -verify <channel>`, exactly as `sb` will, before committing. Each
  `tray/<channel>.json` is signed as `switchboard-tray <channel> <version>` and checked
  with `sb-manifest -tray -verify <channel>`.

Until the variables and secrets exist, the job logs a notice and skips.

**While this repository is private,** release downloads need authentication, so
`sb self-update` can't fetch them. Updates start working once the repository (or
wherever the manifest's URLs point) is public.

### Ramping a rollout

The manifests are signed, so a change needs the release key. In the updates repository,
edit `rollout_percent` in `<channel>.json` (e.g. 10 → 50 → 100) and set `pub_date` to
now, so installs that saw the old manifest can't be served it again. Then re-sign and
check it:

```bash
minisign -S -s switchboard.key -m stable.json -t "sb-manifest stable $(jq -r .pub_date stable.json)"
go run ./cmd/sb-manifest -verify stable -key RW... stable.json   # from this repository
```

For `tray/<channel>.json`, sign with `-t "switchboard-tray <channel> <version>"` and
check with `sb-manifest -tray -verify <channel>`. Commit both files. Clients pick it up
on their next check. To stop a bad release, set
it to 0 the same way. Installs that already updated can run `sb rollback`.

## Testing against a local manifest

```bash
SB_UPDATE_URL=https://localhost:8443/updates sb self-update --check
```

`http://localhost:8080/updates` works too; `http` to any other host is refused.

The tests (`internal/update`, `cmd/sb/selfupdate_test.go`) cover:
- the bucket math and rollout edges;
- every signature failure: wrong key, forged key ID, altered file, altered trusted
  comment, replayed old version, wrong platform;
- manifest signatures: unsigned, another key, changed after signing (rollout, version),
  another channel's, a mismatched `pub_date`, a pre-release on stable, a `pub_date` in
  the future, and an older manifest after a newer one (per channel);
- package-manager detection;
- swap and rollback;
- a round trip from `sb-manifest` to the client;
- a real signature made by Tauri's signer (`internal/update/testdata`, with a
  throwaway key whose secret half was deleted).
