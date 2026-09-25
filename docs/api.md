# Control API

The daemon (`sb daemon`) serves a JSON API over HTTP on a Unix socket at
`<config dir>/sb.sock` (see [config.md](config.md) for the config dir). The CLI and the
GUI use this API. They never edit the config file or system files directly.

- **Access:** the socket has mode `0600`, so only the user who runs the daemon can
  connect. There is no other authentication.
- **One daemon at a time:** if a live daemon already owns the socket, a second daemon
  exits with an error. If the file is only left over from a crash, it is replaced.
- **Windows:** uses the same AF_UNIX socket, which Windows 10 1803 and later support.
- **Versioning:** all paths are under `/v1`. Errors are `{"error": "<message>"}` with a
  4xx or 5xx status.

```bash
curl --unix-socket ~/.config/switchboard/sb.sock http://sb/v1/status
```

## Names

Names without a Switchboard TLD get the default TLD (`test`) appended: `myapp` becomes
`myapp.test`, and `*.tenants.myapp` becomes `*.tenants.myapp.test`. Names are
lowercased and must be LDH hostnames (letters, digits, hyphens), optionally prefixed
with `*.`.

## Endpoints

| Method and path            | Body    | Success                                                                           | Errors                                                         |
| -------------------------- | ------- | --------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| `GET /v1/routes`           | none    | `200`, `[RouteStatus]`                                                            | none                                                           |
| `POST /v1/routes`          | `Route` | `201` if created, `200` if an existing name was updated; the stored `RouteStatus` | `400` for a bad JSON body, unknown field, bad name or bad port |
| `DELETE /v1/routes/{name}` | none    | `200`, the removed `Route`                                                        | `404` if there is no such route                                |
| `GET /v1/status`           | none    | `200`, `Status`                                                                   | none                                                           |
| `GET /v1/events`           | none    | `200`, `text/event-stream`                                                        | none                                                           |

Every change is written to `routes.toml` before it takes effect. If the write fails,
the proxy is rolled back and the request returns `500`.

### Types

```jsonc
// Route
{"name": "myapp.test", "port": 7000, "wildcard": false, "redirect_https": true}

// RouteStatus = Route + health: "up" | "down" | "unknown"
{"name": "myapp.test", "port": 7000, "wildcard": false, "redirect_https": true, "health": "up"}

// Status
{
  "version": "v0.1.0",
  "uptime_seconds": 42,
  "tlds": ["test"],
  "dns":   {"addrs": ["127.0.0.1:15353"], "listening": true},
  "proxy": {"addrs": ["127.0.0.1:80", "[::1]:80"], "listening": false,
            "error": "proxy: listen 127.0.0.1:80: bind: permission denied; ..."},
  "https": {"addrs": ["127.0.0.1:443", "[::1]:443"], "listening": true},
  "routes": [RouteStatus, ...]
}
```

### Health

Every 5 seconds (`--health-interval`), the daemon tries a TCP connection to
`127.0.0.1:<port>` for each route's port, with a 1-second timeout. A new port is checked
right away, and is `unknown` until that first check finishes.

### Events

`GET /v1/events` streams server-sent events. It starts with a `: connected` comment and
sends a `: keepalive` comment every 15 seconds.

```
event: route.added
data: {"type":"route.added","route":{"name":"myapp.test","port":7000,...,"health":"unknown"}}
```

The event types are `route.added`, `route.updated`, `route.removed` and
`health.changed`. A `health.changed` event is sent once for each route on the port
whose health changed. A client that falls more than 32 events behind loses events
rather than slowing down route changes, so reload `GET /v1/status` after reconnecting.

## Daemon startup

`sb daemon` loads `routes.toml`, then claims the socket, then starts the DNS server
(`--dns-addr`, default `127.0.0.1:15353`), the HTTPS proxy (`--https-addr`, default
`127.0.0.1:443,[::1]:443`) and the HTTP proxy (`--http-addr`, default
`127.0.0.1:80,[::1]:80`). `proxy` in the status is plain HTTP; `https` is HTTPS.

If DNS or the proxy cannot bind, for example port 80 without the privileged helper, the
daemon keeps running. `GET /v1/status` and `sb ls` report the error. To try the daemon
without the helper, run:

```bash
sb daemon --http-addr 127.0.0.1:8080
```
