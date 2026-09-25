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

   It then creates the GitHub release with generated notes. Tags with a pre-release
   part (`v0.2.0-rc.1`) are marked as pre-releases.
3. Publishes signed build provenance for every archive and package (GitHub artifact
   attestations). This step is skipped while the repository is private, because GitHub
   does not offer attestations for user-owned private repositories.

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
gh attestation verify sb_0.1.0_darwin_arm64.tar.gz --repo nanaaikinson/switchboard
```

`gh attestation verify` works only for releases built while the repository was public.

## Versioning rules

- Before 1.0, a minor bump (`v0.2.0`) may change the config format or control API. Each
  config change ships a migration (see [config.md](config.md)).
- Patch bumps (`v0.1.1`) are fixes only.
- From 1.0, the config and control API are frozen. Breaking changes need a major bump.
- Never move or delete a published tag. Fix mistakes by releasing a new patch.
