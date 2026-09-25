# Docker routes

With `sb daemon --docker` (the default), every running container that publishes a TCP
port gets a route, with no `sb add`. When the container stops, the route goes. Docker
routes live only in the daemon. They are never written to `routes.toml`.

```bash
docker run -d --name web -p 8080:80 nginx   # → https://web.test
docker compose up -d                         # service "api" in project "shop" → https://api.shop.test
```

`sb ls` lists them with `SOURCE docker (<container>)`. It also lists containers that
publish ports but got no route, and why:

```
NAME             PORT   UPSTREAM  SOURCE
myapp.test       7000   up        config
web.test         8080   up        docker (web)
api.shop.test    49153  up        docker (shop-api-1)

Docker containers without a route:
  shop-worker-1: it publishes 2 ports (5001->5000, 6001->6000) and none is container port 80, 8080 or 3000; set dev.switchboard.port
```

## Names

- **Compose containers** (with `com.docker.compose.project` and
  `com.docker.compose.service` labels): `<service>.<project>.<tld>`.
- **Other containers:** `<container name>.<tld>`.

Names are lowercased, and characters other than letters, digits, dots and hyphens
become hyphens (`My_App` → `my-app.test`). If two containers want the same name (for
example a Compose service scaled to two replicas), the first by container name gets it
and the other is listed as skipped. A route in `routes.toml` always wins over a
container with the same name or wildcard. `sb rm` refuses to remove a Docker route.

## Port choice

Only ports published on all addresses (`0.0.0.0`, `::`) or on `127.0.0.1` count,
because the proxy connects to `127.0.0.1:<host port>`. In order:

1. The `dev.switchboard.port` label: a **container** port. The route uses the host port
   it's published on. A matching host port is accepted too.
2. Else, the only published port.
3. Else, the host port for container port 80, then 8080, then 3000.
4. Else, no route; `sb ls` says why.

Containers that publish no TCP port (databases on an internal network, workers) are
ignored silently. So are containers with `dev.switchboard.enable=false`.

## Labels

| Label | Example | Meaning |
| --- | --- | --- |
| `dev.switchboard.enable` | `false` | No route for this container |
| `dev.switchboard.port` | `9229` | Route to this container port |
| `dev.switchboard.hosts` | `shop,*.shop,admin.shop.test` | These names instead of the default; each gets the default TLD unless it has one |

```yaml
services:
  web:
    image: myshop
    ports: ["3000", "9229"]
    labels:
      dev.switchboard.port: "3000"
      dev.switchboard.hosts: "shop,*.shop"
```

Routes from containers redirect HTTP to HTTPS, like `sb add` routes. There's no label
to turn that off yet.

## Finding Docker

1. `DOCKER_HOST`, if set: `unix://…`, or `tcp://…`. With `DOCKER_TLS_VERIFY`, `tcp://`
   uses `ca.pem`, `cert.pem` and `key.pem` from `DOCKER_CERT_PATH` (default
   `~/.docker`). `ssh://` and `npipe://` aren't supported. Switchboard routes to ports
   on this machine, so a remote engine wouldn't work anyway.
2. Otherwise, the first of these that answers `GET /_ping`:
   - `/var/run/docker.sock`
   - `~/.docker/run/docker.sock` (Docker Desktop)
   - `~/.orbstack/run/docker.sock` (OrbStack)
   - `~/.colima/default/docker.sock`, `~/.colima/docker.sock` (Colima)
   - `~/.rd/docker.sock` (Rancher Desktop)
   - `$XDG_RUNTIME_DIR/podman/podman.sock` (rootless Podman: `systemctl --user enable --now podman.socket`)
   - `/run/podman/podman.sock`
   - `~/.local/share/containers/podman/machine/podman.sock` (Podman machine)

Docker contexts (`docker context use`) aren't read. Set `DOCKER_HOST` for the daemon
if yours isn't on this list.

When no engine is reachable, the daemon retries every 10 seconds, logging only at
debug level. `GET /v1/status` shows why under `docker.error`. When the connection
drops, every Docker route is removed until the daemon reconnects.

## How it works

`internal/docker` uses a small standard-library client for two Engine API endpoints:
`GET /containers/json` and the `GET /events` stream, filtered to container lifecycle
events. The paths are unversioned, so the engine answers in its own API version. That
matters because Docker 29 refuses clients older than API 1.44. It subscribes to
events first, then lists containers, so no change is missed. It lists again after each
burst of events, once they've settled for 250 ms. Turn discovery off with
`sb daemon --docker=false`.
