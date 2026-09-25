# AI Agent Prompts: Switchboard build playbook

Sep 25, 2026 · @Nana Aikinson

## How to use these prompts

Run the prompts in order, one per agent session, with the context file from the next section committed to the repo root first. They are written for coding agents such as Claude Code, but work with any agent that can read the repo and run commands.

1. Commit the context file as `AGENTS.md` (and a `CLAUDE.md` that points to it).
2. Start a fresh session per prompt; paste the prompt as written.
3. Replace anything in `{curly braces}` before sending.
4. Let the agent plan first; approve the plan before it writes code.
5. Review the diff yourself, run the tests, merge, then move to the next prompt.

Rules that apply to every prompt:

- Small pull requests: one prompt, one PR, under about 800 changed lines.
- Never let an agent run `sb setup` or anything with `sudo` on your main machine; use a VM (UTM, Parallels, or a cloud Mac/Windows runner).
- Anything touching the privileged helper, CA or install script gets the security review prompt before merge.
- If an agent gets stuck twice on the same error, stop and debug yourself; don't loop.

## Project context file

Commit this as `AGENTS.md` in the repo root so every agent session starts with the same understanding.

```markdown
# Switchboard — agent context

## What this is
Switchboard maps local ports to trusted HTTPS names: localhost:7000 -> https://myapp.test,
with subdomains and wildcards (api.myapp.test, *.myapp.test). It is a background daemon
with a CLI (`sb`), a web dashboard, and a Tauri tray app, for macOS, Linux and Windows.

## Architecture
- One Go binary, three modes: `sb daemon`, `sb <command>` (CLI), `sb helper` (privileged).
- Daemon: route table, DNS server (127.0.0.1:5353), reverse proxy (:80/:443), local CA,
  control API (JSON over HTTP on a Unix socket / Windows named pipe, versioned /v1).
- Helper: runs as root/SYSTEM. ONLY does: bind 80/443 and pass sockets to the daemon,
  write/remove split-DNS config, install/remove the CA from trust stores. Nothing else.
- CLI and GUI are clients of the control API. They never touch system files directly.
- Proxy is built on net/http/httputil.ReverseProxy behind a Proxy interface; don't add
  proxy libraries (Caddy, Traefik, etc.).

## Layout
cmd/sb/  internal/{dns,proxy,pki,api,config,docker,platform/{darwin,linux,windows}}
ui/dashboard/ (embedded)  app/tray/ (Tauri)  install/  docs/

## Conventions
- Go (latest stable), standard library first. Allowed deps: cobra, miekg/dns,
  smallstep/truststore, Docker SDK, BurntSushi/toml. Ask before adding others.
- OS-specific code only in internal/platform/<os>, behind interfaces; use build tags.
- Errors: wrap with context (fmt.Errorf("...: %w", err)); user-facing messages say what
  to do next.
- Config: TOML at the OS config dir, with schema_version. Never break old configs
  without a migration.
- Logging: log/slog, structured. No secrets or full hostnames at info level.
- Tests: table-driven unit tests; integration tests behind the `integration` build tag.
  Every bug fix adds a test.
- Every system change must have a matching revert in `sb uninstall`.

## Safety rules for agents
- Never run commands with sudo, never run `sb setup` or `sb helper`, never edit
  /etc, the hosts file, trust stores or system services on the host machine.
- Never commit keys, certificates or signing credentials.
- Default TLD is `.test`. `.local` is opt-in mDNS mode only.
- Bind to loopback only unless the code path is explicit LAN mode.

## Commands
- Build: `go build ./cmd/sb`
- Test: `go test ./...`  |  Integration: `go test -tags integration ./...`
- Lint: `golangci-lint run`  |  Vulns: `govulncheck ./...`

## Definition of done
Code + tests + docs (docs/ or --help text) + passes lint and tests + no new deps
without approval + uninstall path covered.
```

## Milestone v0.1 prompts (macOS CLI)

Six prompts build the first usable release: routes, DNS, HTTP proxy and install script on macOS.

### P1 — Scaffold the repo

```
Read AGENTS.md. Scaffold the Go module `github.com/{you}/switchboard` with the layout it
describes. Create cmd/sb/main.go with cobra and three top-level modes: `daemon`,
`helper`, and CLI subcommands (add, rm, ls, open, doctor, setup, uninstall) that print
"not implemented" for now. Add internal/config with a Route type {Name, Port, Wildcard,
RedirectHTTPS} and TOML load/save with schema_version=1 at the OS config dir, with
unit tests. Add a Makefile (build, test, lint), .golangci.yml, and a GitHub Actions
workflow running build/test/lint on macos-latest and ubuntu-latest.
Propose the plan first; don't write code until I approve.
```

### P2 — DNS server

```
Read AGENTS.md. Implement internal/dns: a DNS server using miekg/dns that listens on a
configurable address (default 127.0.0.1:5353, UDP and TCP) and answers A with 127.0.0.1
and AAAA with ::1 for any name under the configured TLDs (default ["test"]), including
the bare TLD's subdomains at any depth. Everything else returns REFUSED; it must never
forward queries. Support changing TLDs at runtime without restart. Write unit tests
using an in-process client covering: exact name, deep subdomain, other TLD refused,
case-insensitivity, TCP fallback. Keep it under ~300 lines.
```

### P3 — Reverse proxy and route table

```
Read AGENTS.md. Implement internal/proxy using httputil.ReverseProxy from the standard
library, wrapped in a Proxy interface so it can be swapped later. It listens on a
configurable address (default 127.0.0.1:80 plus [::1]:80) that routes by Host header
using the route table from internal/config. Matching: exact name > longest wildcard
(*.myapp.test) > 404 page listing configured routes. Upstream is http://127.0.0.1:<port>.
Requirements: WebSocket upgrades, streaming responses (SSE), X-Forwarded-Proto/Host/For
headers, route table swaps atomically at runtime, friendly 502 page when the upstream
port is closed that names the port. Include tests with httptest upstreams, including a
WebSocket echo test and a concurrent route-swap test.
```

### P4 — Daemon and control API

```
Read AGENTS.md. Implement `sb daemon`: it loads config, starts internal/dns and
internal/proxy, and serves a control API over a Unix socket in the user config dir
(mode 0600). Endpoints under /v1: GET /routes, POST /routes, DELETE /routes/{name},
GET /status (version, uptime, dns/proxy listening state, per-route upstream up/down),
GET /events (SSE stream of route and health changes). Health-check each upstream port
every 5s with a TCP dial. Persist every change to config. Then wire the CLI commands
add/rm/ls/open to this API with a small client package; `ls` prints a table and supports
--json. Names given without the TLD get the default TLD appended.
```

### P5 — macOS platform layer and helper

```
Read AGENTS.md. Implement internal/platform/darwin behind a Platform interface with:
InstallResolver(tld, port), RemoveResolver(tld), InstallService(), RemoveService(),
and the helper protocol. `sb setup` must: print exactly what it will change, ask for
confirmation, then (via a single sudo invocation of `sb helper install`) write
/etc/resolver/<tld> ("nameserver 127.0.0.1\nport 5353"), install a LaunchDaemon for the
helper that binds :80 and passes the listener to the user daemon over its socket, and a
LaunchAgent for `sb daemon`. `sb uninstall` reverses every step and is idempotent.
Write the code and tests for file generation, but DO NOT run setup, sudo, or launchctl
yourself. Give me a manual test checklist to run in a macOS VM.
```

### P6 — Doctor and install script

```
Read AGENTS.md. (1) Implement `sb doctor`: checks daemon running, helper running,
resolver file present and correct, `dscacheutil -q host -a name probe.<tld>` resolves to
127.0.0.1, ports 80/443 owned by us (else name the process holding them), each route's
upstream reachable. Print pass/fail lines with a one-line fix for each failure; exit
code 1 on any failure. (2) Write install/install.sh (POSIX sh, shellcheck-clean): detect
OS/arch, download the matching release asset and SHA256SUMS from GitHub Releases,
verify the checksum (and minisign signature if minisign is present), install to
~/.local/bin (or /usr/local/bin with --global), warn if not on PATH, then tell the user
to run `sb setup`. Support SB_VERSION to pin a version. Add a CI job running shellcheck.
```

## Milestone v0.2–v0.3 prompts (HTTPS, Linux, Docker)

Four prompts add trusted HTTPS, Linux support, Docker discovery and committed project config.

### P7 — Local CA and HTTPS

```
Read AGENTS.md. Implement internal/pki: generate an ECDSA P-256 root CA on first run
(10-year validity) with X.509 name constraints permitting only the configured TLDs;
store the key with 0600 permissions (use the macOS Keychain if straightforward, else
file). Issue leaf certificates on demand via tls.Config.GetCertificate: one cert per
exact name, and wildcard certs for *.<parent> when a wildcard route exists; cache in
memory and on disk; 90-day leaves renewed at 30 days left. Add :443 to the proxy with
HTTP/2, and HTTP->HTTPS redirect per route (default on). Extend the helper protocol with
TrustCA/UntrustCA using smallstep/truststore (system + NSS). Add `sb trust` and
`sb untrust`. Tests: name constraints reject google.com, wildcard issuance, renewal.
Then add HTTPS checks to `sb doctor`.
```

### P8 — Linux platform layer

```
Read AGENTS.md. Implement internal/platform/linux with the same Platform interface as
darwin. Split DNS: if systemd-resolved is active, write
/etc/systemd/resolved.conf.d/switchboard-<tld>.conf with DNS=127.0.0.1:5353 and
Domains=~<tld>, then restart resolved; otherwise, if NetworkManager uses dnsmasq, write a
dnsmasq drop-in; otherwise fall back to hosts-file entries for exact names and warn that
wildcards won't work. Services: systemd system unit for the helper, user unit for the
daemon. Trust: truststore handles distro bundles and NSS. Add .deb/.rpm packaging via
nfpm in .goreleaser.yaml. Add an integration test job in CI that runs setup inside a
privileged Ubuntu container and curls https://probe.test. Don't run sudo on the host.
```

### P9 — Docker auto-discovery

```
Read AGENTS.md. Implement internal/docker: connect to the Docker socket (respect
DOCKER_HOST; also probe OrbStack, Colima and Podman default socket paths), list running
containers and subscribe to events. For each container with a published TCP port,
register an ephemeral route: <container>.<tld>, or <service>.<project>.<tld> when the
com.docker.compose labels exist. Port choice: label dev.switchboard.port, else the
single published port, else the published port mapping to 80/8080/3000 in that order,
else skip and surface why in `sb ls`. Labels: dev.switchboard.hosts (comma list),
dev.switchboard.enable=false. Docker routes are not persisted and show source=docker in
the API. If Docker isn't running, retry quietly every 10s. Tests with a fake Docker client.
```

### P10 — Project config file

```
Read AGENTS.md. Add `sb apply [path]` which reads switchboard.toml (default: search the
current dir upward to the git root). Format: [routes] table of "name" = port or
{ port = 3000, redirect = false }. Routes from a file are tagged with the file path as
source; `sb apply --down` removes them; re-applying replaces only that file's routes.
Warn on names conflicting with existing routes from other sources and don't overwrite
them. Add `sb init` that writes a commented example file. Tests for conflict handling and
idempotency. Document the format in docs/project-config.md.
```

## Milestone v0.4–v0.5 prompts (GUI, updates, Windows)

Five prompts add the dashboard, tray app, self-update and Windows support.

### P11 — Web dashboard

```
Read AGENTS.md. Build ui/dashboard with {Svelte | React} + Vite + TypeScript. It talks
only to the control API, exposed by the daemon at https://switchboard.<tld> (add this as a
built-in route served by the daemon, protected by a random token in an HttpOnly cookie
set when opened via `sb dashboard`). Screens: route list (name as link, port, source,
status dot, toggle redirect, delete), add-route form with validation, live updates via
/v1/events, per-route recent log lines, settings (TLDs, trust status). Light/dark via
prefers-color-scheme. Embed the built assets into the Go binary with embed.FS.
Keep it clean and fast; no heavy UI kit. Add Playwright smoke tests against a fake API.
```

### P12 — Tauri tray app

```
Read AGENTS.md. Create app/tray as a Tauri v2 app. It ships the `sb` binary as a
sidecar and on first launch runs a wizard: explain what setup changes, then run
`sb setup` (admin prompt via the OS). Tray menu: routes with status (from /v1/status,
refreshed via /v1/events), click to open in browser, "Add route...", "Open dashboard"
(opens the dashboard in a Tauri window), "Pause all", "Install command-line tool",
"Check for updates", "Quit". Start at login toggle. macOS: menu-bar only, no Dock icon.
Configure bundling for .dmg (macOS) and NSIS + MSI (Windows) with signing read from env
vars; don't include any credentials. List the manual QA steps for macOS.
```

### P13 — Self-update and update manifests

```
Read AGENTS.md. Implement `sb self-update [--channel stable|beta]` and `sb rollback`.
The update manifest (JSON at https://updates.{domain}/{channel}.json) lists version,
per-platform URLs, SHA256, minisign signature, and rollout_percent. Client: compute a
stable bucket from a random install ID stored in config; only update if bucket <
rollout_percent. Verify signature with an embedded public key, download to temp,
verify hash, atomically replace the binary keeping the previous one as sb.old, restart
the daemon via the platform service. Refuse and explain if installed by Homebrew,
winget, Scoop or a Linux package (detect by path/receipt). Also configure the Tauri
updater plugin to use the same manifest host. Tests for bucket logic and signature failure.
```

### P14 — Windows platform layer

```
Read AGENTS.md. Implement internal/platform/windows: control API over a named pipe
with an ACL for the current user; helper as a Windows service (golang.org/x/sys/windows/svc)
that binds 127.0.0.1:53, :80 and :443 and hands listeners to the user daemon; split DNS
via an NRPT rule (Add-DnsClientNrptRule -Namespace .<tld> -NameServers 127.0.0.1) and
removal on uninstall; CA install into LocalMachine\Root via truststore; daemon started
per user via a Scheduled Task at logon. Detect port conflicts with IIS / http.sys
(netsh http show servicestate) and report them in `sb doctor`. Write install/install.ps1
mirroring install.sh. Add windows-latest to CI. Don't modify the host system; give me a
VM test checklist.
```

### P15 — .local mDNS mode (experimental)

```
Read AGENTS.md. Add an opt-in `.local` mode: `sb tld add local --mdns`. When enabled, the
daemon announces an A/AAAA record (127.0.0.1 / ::1) over mDNS for every registered route
under .local, re-announcing on changes and withdrawing on removal, using the native
responder where possible (dns_sd on macOS, Avahi D-Bus on Linux, fallback to a Go mDNS
library). Announce only on the loopback interface by default. Wildcard routes can't be
announced; `sb ls` and the dashboard must say so. `sb doctor` detects when .local queries
are leaking to unicast DNS and explains the risk. Mark the feature experimental everywhere.
```

## Release, CI and signing prompts

Three prompts automate releases end to end; you supply the secrets, the agent never sees them.

### R1 — GoReleaser and Homebrew tap

```
Read AGENTS.md. Add .goreleaser.yaml building sb for darwin/linux/windows x amd64/arm64,
with -trimpath and version ldflags, archives, SHA256SUMS, minisign signing of the
checksum file (key from the MINISIGN_KEY secret), nfpm .deb/.rpm with the systemd units,
and a Homebrew formula published to {you}/homebrew-tap (token from HOMEBREW_TAP_TOKEN).
Add release-please for conventional-commit changelogs and version PRs. Add a
.github/workflows/release.yml triggered by tags v*. Beta tags (-beta.N) publish as
prereleases and skip the Homebrew stable formula. Document the required secrets in
docs/releasing.md without any values.
```

### R2 — macOS signing and notarization

```
Read AGENTS.md. Add a macOS job to release.yml that imports a Developer ID certificate
from secrets into a temporary keychain, signs the sb binary and the Tauri .app with
hardened runtime and required entitlements, notarizes with `xcrun notarytool` using an
App Store Connect API key, staples the ticket, builds the .dmg, and publishes a Homebrew
cask. Clean up the keychain at the end even on failure. List every secret name needed and
how I create each one in Apple's portals.
```

### R3 — Windows signing and packaging

```
Read AGENTS.md. Add a Windows job to release.yml that signs sb.exe, the Tauri app and
the installer with {signing provider: cloud-signing CA certificate | Azure Artifact
Signing}, using timestamping, with credentials only from secrets. Publish the MSI/NSIS
installer to the release, generate a winget manifest PR (wingetcreate) and update a
Scoop bucket. Verify signatures with signtool in the job before upload. Document setup
steps in docs/releasing.md.
```

## Recurring prompts

Use these whenever the situation comes up, not in a fixed order.

### Code review

```
Read AGENTS.md, then review the diff on branch {branch} against main. Check: correctness,
error handling, races (run go test -race on touched packages), OS-specific code outside
internal/platform, missing tests, missing uninstall path, user-facing messages that don't
say what to do next, new dependencies. Report findings as a list ordered by severity
(blocker / should fix / nit) with file:line. Don't change code.
```

### Security review (required for helper, PKI, install script, updater)

```
Read AGENTS.md. Do a security review of {paths}. Threat model: a local unprivileged
attacker or a malicious web page. Check: helper accepts only its fixed operations and
validates every argument (path traversal, injection into shell or config files); socket
and file permissions; CA name constraints and key storage; anything binding beyond
loopback; DNS rebinding and Host header handling; download and signature verification
in install/update paths; secrets in logs. For each issue give severity, a concrete
exploit scenario, and the fix. Don't change code.
```

### Bug triage from an issue

```
Read AGENTS.md. Here is a user report and their `sb diag` output: {paste}. Identify the
likely root cause, which component it's in, and whether it's OS-specific. Write a failing
test that reproduces it, then the minimal fix, then confirm the test passes. If you can't
reproduce it, say what extra information to ask the user for, as a reply I can paste.
```

### Docs for a feature

```
Read AGENTS.md and the code for {feature}. Write docs/{feature}.md for users: what it does
in one sentence, a copy-paste example, all options, common problems and fixes. Plain
language, short sentences, no marketing. Update `--help` text if it disagrees with the docs.
```

### Release notes

```
Read the commits and merged PRs between {previous tag} and {new tag}. Write release notes
for users: highlights (max 3), new features, fixes, breaking changes with migration
steps, known issues. Skip internal refactors. Keep it under 250 words.
```

### OS upgrade check

```
Read AGENTS.md. {OS name and version} just shipped. Search the release notes and
developer forums for changes to DNS resolution, split-DNS configuration, trust stores,
launchd/service management, and loopback networking. List anything that could affect
Switchboard, what to test in a VM, and proposed code changes if needed.
```
