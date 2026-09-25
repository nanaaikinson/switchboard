# Switchboard — agent context

## Prodct Specification

- Reference: docs/product-specification.md

## What this is

Switchboard maps local ports to trusted HTTPS names: localhost:7000 -> https://myapp.test,
with subdomains and wildcards (api.myapp.test, \*.myapp.test). It is a background daemon
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

cmd/sb/ internal/{dns,proxy,pki,api,config,docker,platform/{darwin,linux,windows}}
ui/dashboard/ (embedded) app/tray/ (Tauri) install/ docs/

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
- Test: `go test ./...` | Integration: `go test -tags integration ./...`
- Lint: `golangci-lint run` | Vulns: `govulncheck ./...`

## Definition of done

Code + tests + docs (docs/ or --help text) + passes lint and tests + no new deps
without approval + uninstall path covered.
