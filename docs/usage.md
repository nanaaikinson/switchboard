# Using Switchboard

This guide covers everyday use. It assumes you've run `sb setup`
([Getting started](getting-started.md)).

## Routes

A route gives a name to an app on `127.0.0.1:<port>`.

```bash
sb add myapp 3000          # https://myapp.test -> 127.0.0.1:3000
sb add api.myapp 3001      # https://api.myapp.test -> 127.0.0.1:3001
sb ls                      # list routes and whether each app is up
sb open myapp              # open it in your browser
sb rm api.myapp            # remove a route
```

- **The `.test` ending is optional.** `sb add myapp 3000` and `sb add myapp.test 3000`
  do the same thing.
- **Adding a name again updates it.** `sb add myapp 4000` moves `myapp.test` to port
  4000.
- **HTTP redirects to HTTPS** by default. To serve plain HTTP as well, add
  `--no-redirect`.
- **Names** are letters, digits, hyphens and dots, like DNS names. `switchboard.test` and
  everything under it is reserved for the dashboard. Ports 80 and 443 are refused,
  because Switchboard listens there itself.
- **`sb ls`** shows each route's port, whether its app answers (`up`, `down`, or
  `unknown` before the first check, every 5 seconds), and where the route comes from:
  `config` (`sb add`), a project file, or a Docker container. `sb ls --json` prints the
  same as JSON.

Routes you add are saved and come back after a restart.

### Wildcards

A wildcard route answers for every subdomain:

```bash
sb add '*.tenants.myapp' 3000    # acme.tenants.myapp.test, beta.tenants.myapp.test, ...
sb add myapp 3000 --wildcard     # myapp.test and every *.myapp.test
```

Quote names that start with `*`, so your shell doesn't expand them. An exact route
always wins over a wildcard. Among wildcards, the longest match wins.

Each subdomain under a wildcard gets a proper certificate. On Linux systems without
systemd-resolved or NetworkManager's dnsmasq, only exact names resolve, so wildcards
don't work there. `sb doctor` and [setup-linux.md](setup-linux.md) say which mode you're
in.

### What your app sees

Requests arrive at your app over plain HTTP on its port, with the `Host` header set to
the name (`myapp.test`). Switchboard adds `X-Forwarded-Proto`, `X-Forwarded-Host` and
`X-Forwarded-For`, and removes any a client sent. WebSockets and server-sent events
work.

If the app isn't running, the browser shows a Switchboard page that names the port it
tried. A name with no route shows a page that lists your routes.

## Project files

Put the routes a project needs in a `switchboard.toml` in its repository. Everyone who
clones it gets the same names:

```bash
sb init        # writes a commented example in the current directory
sb apply       # adds or updates the file's routes
sb apply --down
```

```toml
[routes]
"shop" = 3000
"api.shop" = { port = 4000, redirect = false }
"*.tenants.shop" = 3000
```

`sb apply` finds the nearest `switchboard.toml` in the current directory or its parents,
up to the git root. Run it again after you edit the file: it adds, updates and removes
only that file's routes. It never overwrites a route from `sb add`, another project or
Docker; it skips it with a warning. Full format: [Project files](project-config.md).

## Docker

Running containers that publish a TCP port get a route automatically, with no `sb add`:

```bash
docker run -d --name web -p 8080:80 nginx   # https://web.test
docker compose up -d                         # service "api" in project "shop": https://api.shop.test
```

The route goes away when the container stops. Choose names and ports with labels:

| Label | Does |
| --- | --- |
| `dev.switchboard.hosts=shop,admin.shop` | use these names instead |
| `dev.switchboard.port=3000` | use this container port, when it publishes several |
| `dev.switchboard.enable=false` | no route for this container |

`sb ls` lists containers that publish ports but got no route, and why. Docker Desktop,
OrbStack, Colima, Rancher Desktop and Podman are found automatically. Details:
[Docker routes](docker.md).

## The dashboard

```bash
sb dashboard
```

This opens `https://switchboard.test` in your browser, signed in. It shows your routes
live, whether each app is up, the recent requests to each route, and whether the
certificate authority is trusted. You can add, change and remove routes there too.

The sign-in link works once, for two minutes. Opening the dashboard any other way asks
you to run `sb dashboard`. `sb dashboard --print` prints the link instead. More:
[Dashboard](dashboard.md).

## The menu-bar app (macOS)

The Switchboard app lives in the menu bar. It bundles `sb`, and on first launch it walks
you through setup. From the menu you can:

- see every route and whether its app is up, and click one to open it;
- add a route;
- open the dashboard;
- pause every route;
- install the `sb` command for your terminal;
- check for updates;
- start the app at login.

Quitting the app doesn't stop the daemon. More: [Menu-bar app](tray.md).

## Pausing

```bash
sb pause     # every route answers "Switchboard is paused" (503)
sb resume
```

Nothing is removed while paused, and the dashboard keeps working. Restarting the daemon
resumes.

## `.local` names (experimental)

You can also use `.local` names. They're announced over multicast DNS (mDNS) on the
loopback interface, through mDNSResponder on macOS, Avahi on Linux, and a built-in
responder on Windows:

```bash
sb tld add local --mdns
sb add myapp.local 3000
```

Wildcards can't be announced over mDNS, so add each `.local` name you need. Some
networks answer `.local` through normal DNS, which can confuse apps; `sb doctor` checks
for this. For HTTPS, the certificate authority must cover `.local`. `sb tld add` tells
you if yours doesn't. Turn it off with `sb tld rm local`. More: [.local mode](mdns.md).

## Updating

```bash
sb self-update           # install the latest stable release
sb self-update --check   # only check
sb rollback              # go back to the previous version
```

Every download is checked against its signature before anything is replaced. If you
installed `sb` with Homebrew, a `.deb` or `.rpm`, Scoop or winget, update it with that
tool instead; `sb self-update` tells you which. The menu-bar app updates itself from
**Check for Updates…**.

After updating, run `sb setup` again, so the privileged helper gets the new version too.
`sb doctor` reminds you. More: [Updates](updates.md).

## Trusting the certificate authority again

`sb setup` trusts Switchboard's certificate authority. If you install Firefox (or
another browser with its own store) later, run:

```bash
sb trust
```

`sb untrust` removes the trust; HTTPS names show warnings until you run `sb trust`
again. The certificate authority can only sign certificates for Switchboard's names,
never for real websites. More: [HTTPS and the local CA](https.md).

## Where Switchboard keeps its files

| | macOS and Linux | Windows |
| --- | --- | --- |
| Routes, certificate authority, settings | `~/.config/switchboard` | `%APPDATA%\switchboard` |
| Daemon log | macOS: `~/Library/Logs/switchboard-daemon.log`; Linux: `journalctl --user -u switchboard` | none; stop the logon task and run `sb daemon` in a terminal to see its output |

Set `SWITCHBOARD_CONFIG_DIR` to use another folder. On Linux, `XDG_CONFIG_HOME` is
honoured too. The route file's format: [Route file](config.md).

## All commands

| Command | Does |
| --- | --- |
| `sb setup` | one-time system setup |
| `sb add <name> <port>` | add or update a route (`--wildcard`, `--no-redirect`) |
| `sb rm <name>` | remove a route |
| `sb ls` | list routes and their status (`--json`) |
| `sb open <name>` | open a route in the browser |
| `sb init` / `sb apply` | create and apply a project's `switchboard.toml` (`apply --down` removes) |
| `sb dashboard` | open the web dashboard (`--print` for the link) |
| `sb pause` / `sb resume` | turn every route off and on |
| `sb tld ls` / `add` / `rm` | list TLDs; turn the experimental `.local` mode on and off |
| `sb doctor` | check the setup and suggest fixes |
| `sb trust` / `sb untrust` | trust or stop trusting the local certificate authority |
| `sb self-update` / `sb rollback` | update `sb`, or undo the last update |
| `sb uninstall` | remove every system change `sb setup` made |

`sb <command> --help` explains each one. `sb setup`, `sb uninstall`, `sb trust` and
`sb untrust` also take `--print-plan` (show the changes, make none) and `--yes` (don't
ask).
