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
- **Forwarded headers:** any `Forwarded`, `X-Real-IP` and `X-Forwarded-*` headers the
  client sent are dropped, so an app can trust them. Then
  `X-Forwarded-For` (client IP), `X-Forwarded-Host` (original host) and
  `X-Forwarded-Proto` (`http` or `https`) are set.
- **Loop protection:** every proxied request carries `X-Switchboard-Hop: <host>`,
  replacing any the client sent. A request that arrives with that header for its own
  host has come back from a route pointing at the proxy's own port (`myapp.test` → 80),
  so it's answered with `508 Loop Detected` instead of being proxied again. Apps that
  call another route and copy their incoming headers still work, because the host
  differs.
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
| The route's port is the proxy's own, so the request came back | 508 | Says the route loops and to point it at the app's port |

## HTTPS

- **Serving:** `ServeTLS` serves the same handler over TLS 1.2+, with certificates from
  `pki.Issuer` (see [https.md](https.md)). It offers HTTP/2 and HTTP/1.1 through ALPN.
  WebSockets use HTTP/1.1.
- **Redirects:** on the plain-HTTP listeners, the daemon wraps the proxy in
  `RedirectHTTPS`. A request for a route with `redirect_https = true` gets a
  `307 Temporary Redirect` to the same path and query over HTTPS. 307 keeps the method
  and body, and browsers don't cache it, so `--no-redirect` takes effect immediately.
  Only hosts that are valid hostnames are redirected. Other requests, and every request
  while HTTPS isn't listening, are proxied over plain HTTP as before.

## Listening

- **Default addresses:** `127.0.0.1:80` and `[::1]:80` for HTTP, and `127.0.0.1:443` and
  `[::1]:443` for HTTPS. Only loopback IPs are accepted.
- **Ports 80 and 443:** they are privileged. In production the helper binds them and
  passes the listeners to `Serve` and `ServeTLS`.
- **Route changes:** `SetRoutes` swaps the whole table atomically. In-flight requests
  finish on the table they started with. An invalid table is rejected and the old one
  stays in place.

```go
p, err := proxy.New(cfg.Routes)
lns, err := proxy.Listen(nil) // or listeners passed from the helper
go proxy.Serve(ctx, p, lns)   // graceful shutdown when ctx is done
_ = p.SetRoutes(newRoutes)
```

Plain-HTTP requests for a route with `redirect_https` get a `307` to the same URL over
HTTPS, while the HTTPS listener is up (`proxy.RedirectHTTPS`). It's on by default.
