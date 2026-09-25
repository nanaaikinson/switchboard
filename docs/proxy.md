# HTTP proxy

`internal/proxy` routes HTTP requests by `Host` header to apps on local ports. It is
built on `net/http/httputil.ReverseProxy` behind the `Proxy` interface, so the
implementation can be swapped later.

## Matching

The `Host` header is lowercased, and any port and trailing dot are removed. Matches are
tried in this order:

1. **Exact name:** `api.myapp.test` → the route named `api.myapp.test`.
2. **Longest wildcard:** for `a.tenants.myapp.test`, `*.tenants.myapp.test` beats
   `*.myapp.test`. A wildcard never matches its own base name, so
   `*.tenants.myapp.test` does not match `tenants.myapp.test`.
3. **Otherwise:** a 404 page.

A route is a wildcard when its name starts with `*.` (`*.myapp.test`). A route can also
set `wildcard = true` (see [config.md](config.md)), which makes it match both its own
name and every subdomain of it. Two routes that claim the same exact name or the same
wildcard are rejected.

## Upstream requests

- **Target:** `http://127.0.0.1:<port>`. HTTP proxy environment variables are ignored.
- **Host header:** the original `Host` is passed through, so dev servers generate
  correct URLs.
- **Forwarded headers:** any `X-Forwarded-*` headers the client sent are dropped. Then
  `X-Forwarded-For` (client IP), `X-Forwarded-Host` (original host) and
  `X-Forwarded-Proto` (`http` or `https`) are set.
- **WebSockets and upgrades:** `Connection: Upgrade` requests are tunnelled both ways.
- **Streaming:** server-sent events (`text/event-stream`) and responses without a
  `Content-Length` are flushed to the client as they arrive.
- **Timeouts:** the server has no read or write timeout, so long-lived streams stay
  open. Headers must arrive within 10 seconds.

## Error pages

| Case | Status | Page |
| --- | --- | --- |
| No route, and the host is under a routed TLD | 404 | Lists the configured routes and suggests `sb add <host> <port>` |
| No route, and the host is under any other TLD | 404 | Shows no routes. A DNS-rebinding page (`evil.com` → 127.0.0.1) cannot read the route table. |
| Route found but nothing listening on the port | 502 | "Nothing is listening on port N", with next steps |
| Upstream closed the connection or sent an invalid response | 502 | Names the port and suggests checking the app's logs |

## Listening

- **Default addresses:** `127.0.0.1:80` and `[::1]:80`. Only loopback IPs are accepted.
- **Port 80:** it is privileged. In production the helper binds it and passes the
  listeners to `Serve`.
- **Route changes:** `SetRoutes` swaps the whole table atomically. In-flight requests
  finish on the table they started with. An invalid table is rejected and the old one
  stays in place.

```go
p, err := proxy.New(cfg.Routes)
lns, err := proxy.Listen(nil) // or listeners passed from the helper
go proxy.Serve(ctx, p, lns)   // graceful shutdown when ctx is done
_ = p.SetRoutes(newRoutes)
```

HTTP to HTTPS redirects (`redirect_https`) are not applied yet. They arrive with TLS
in v0.2.
