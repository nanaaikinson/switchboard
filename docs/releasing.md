# Releasing

Releases are cut by pushing a [semver](https://semver.org) tag. The
[release workflow](../.github/workflows/release.yml) then:

1. Runs the full CI workflow (build, test, lint on macOS and Ubuntu).
2. Rejects the tag unless it matches `vMAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]`.
3. Runs `make dist`, which cross-compiles `sb` for darwin, linux and windows on
   amd64 and arm64 with `main.version` set to the tag, then writes
   `checksums.txt` (SHA-256).
4. Publishes signed build provenance for every archive (GitHub artifact attestations).
   This step is skipped while the repository is private, because GitHub does not offer
   attestations for user-owned private repositories.
5. Creates a GitHub release with generated notes. Tags with a pre-release part
   (`v0.2.0-rc.1`) are marked as pre-releases.

## Cutting a release

```bash
git switch main && git pull
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Build the same archives locally with `make dist VERSION=v0.1.0`.

Verify a downloaded archive:

```bash
shasum -a 256 -c checksums.txt --ignore-missing
gh attestation verify sb_0.1.0_darwin_arm64.tar.gz --repo nanaaikinson/switchboard
```

`gh attestation verify` works only for releases built while the repository was public.

## Versioning rules

- Before 1.0, a minor bump (`v0.2.0`) may change the config format or control API. Each
  config change ships a migration (see [config.md](config.md)).
- Patch bumps (`v0.1.1`) are fixes only.
- From 1.0, the config and control API are frozen. Breaking changes need a major bump.
- Never move or delete a published tag. Fix mistakes by releasing a new patch.
