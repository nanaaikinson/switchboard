# Switchboard documentation

## Guides

For people using Switchboard.

- [Getting started](getting-started.md): install, set up, name your first app
- [Using Switchboard](usage.md): routes, wildcards, project files, Docker, the dashboard,
  the menu-bar app, updates, and every command
- [Troubleshooting](troubleshooting.md): `sb doctor`, common problems, questions
- [Uninstalling](uninstall.md): remove Switchboard completely

## Reference

- [Installing](install.md): every install method and option
- [Project files](project-config.md): the `switchboard.toml` format
- [Docker routes](docker.md): names, ports and labels
- [Dashboard](dashboard.md) and [menu-bar app](tray.md)
- [.local mode](mdns.md) (experimental)
- [Updates](updates.md): `sb self-update`, `sb rollback`, and how releases are signed

## How it works

- What `sb setup` changes on each system, and the security design:
  [macOS](setup-macos.md), [Linux](setup-linux.md), [Windows](setup-windows.md)
- [DNS server](dns.md), [proxy](proxy.md), [HTTPS and the local CA](https.md)
- [Control API](api.md) and [route file](config.md)

## For maintainers

- [Releasing](releasing.md) and the [backlog](backlog.md)
- [Product specification](product-specification.md)
