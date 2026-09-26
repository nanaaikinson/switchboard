# Web dashboard

```bash
sb dashboard          # opens https://switchboard.test in your browser
sb dashboard --print  # prints the sign-in link instead, e.g. to open it elsewhere
```

The daemon serves the dashboard itself at `https://switchboard.<tld>`. That name and
every name under it (`api.switchboard.<tld>`, `*.switchboard.<tld>`) are reserved:
`sb add`, `sb apply` and Docker can't take them, and a broad wildcard route such as
`*.test` doesn't serve them either. Such routes saved by an older version stay in
`routes.toml` but are not served; the daemon logs a warning, and you can remove them
with `sb rm`. What it shows:

- **Routes:** each route's status (up, down, not checked yet), its name as a link, its
  port and where it comes from (`sb add`, a `switchboard.toml`, or a Docker container).
  Updates arrive live from `/v1/events`.
- **HTTPS:** every route is served over HTTPS; there's nothing to enable. A lock next to
  each name shows whether that works right now, and its tooltip names the certificate
  served (`myapp.test`, plus `*.myapp.test` for routes that match subdomains):
  - **green:** HTTPS is up and the system trusts the local CA.
  - **amber:** HTTPS works but browsers will warn. A banner says to run `sb trust`, with
    a *Check again* button.
  - **open:** the HTTPS proxy isn't running. A banner shows the daemon's error and
    points to `sb doctor`, and links switch to `http://` so they still work.

  **HTTPS** ("Serve this over TLS"; per route, on by default) decides whether
  `http://` requests are redirected to `https://`. Turned off, the route answers plain
  `http://` as well; `https://` keeps working either way.
- **.local mode (experimental):** with `sb tld add local --mdns` on, a banner says the
  mode is experimental and how many names are announced over mDNS, or why none are.
  Each `.local` route gets an **mDNS** badge (*mDNS*, *mDNS pending*, or *not on mDNS*
  for wildcards, which can't be announced). See [mdns.md](mdns.md).
- **Editing:** toggle HTTPS, delete routes, and add routes with validation.
  Routes from project files and containers are read-only here, because the file or the
  container's labels own them. A tooltip says where to change them.
- **Recent requests:** the last 100 requests to each route (time, method, path, status,
  duration). They're kept in memory, never written to disk, and query strings are
  never stored.
- **Settings:** TLDs, whether the system trusts the local CA (checked by verifying a
  certificate against the system trust store, as a browser does), the listeners, and
  Docker discovery.

The theme follows the OS (`prefers-color-scheme`) until you pick **Light** or **Dark**
with the toggle in the header; **Match system** goes back. The choice is stored in the
browser (`localStorage`, key `switchboard-theme`), and `public/theme.js` applies it
before the first paint, as a file because the CSP forbids inline scripts.

## Security

The dashboard can change routes, so only you can open it:

1. `sb dashboard` asks the daemon, over the control socket (mode 0600, yours only),
   for a **one-time sign-in token**. It works once, within two minutes, and is only
   ever handed out on that socket.
2. It opens `https://switchboard.<tld>/login?token=…`. The daemon exchanges the token
   for a session cookie, `__Host-sb_session`: random, new each time the daemon starts,
   `HttpOnly; Secure; SameSite=Strict; Path=/`, with no `Domain`. The `__Host-` prefix
   means browsers only accept it from the dashboard itself, so a page on another
   `.test` name can't plant or overwrite it. It then redirects to `/`, so the token doesn't
   stay in the address bar. Every other request without the cookie gets a
   "run `sb dashboard`" page.
3. The dashboard is only served over HTTPS. Plain HTTP only redirects.
4. The browser can reach an **allowlist** of the control API: `GET /v1/routes`,
   `/v1/status`, `/v1/events`, `/v1/ca` and `/v1/routes/{name}/logs`, plus
   `POST /v1/routes` and `DELETE /v1/routes/{name}`. `POST /v1/apply` and the sign-in
   endpoint stay socket-only.
5. Writes must come from the dashboard's own page: the `Origin` must be
   `https://switchboard.<tld>[:port]`, and there must be an
   `X-Requested-With: switchboard` header. So another `*.test` app (a different site
   to the browser) can't use your session, and neither can a DNS-rebinding page.
6. Responses carry a strict Content Security Policy (`script-src 'self'`, no framing),
   `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.

## Development

The app is in [ui/dashboard](../ui/dashboard): React 19, TypeScript, Vite,
[TanStack Router](https://tanstack.com/router), Tailwind CSS 4 and
[shadcn/ui](https://ui.shadcn.com) components (copied into `src/components/ui`, not a
dependency). It only talks to the control API ([api.md](api.md)).

```bash
cd ui/dashboard
npm ci
npm run fake-api &   # a fake control API on :5199, with sample routes
npm run dev          # Vite on :5173, proxying /v1 to the fake API
npm test             # builds, then runs the Playwright smoke tests against the fake API
```

The built files in `ui/dashboard/dist` are **committed**, because `sb` embeds them
with `embed.FS` ([embed.go](../ui/dashboard/embed.go)) and `go build` shouldn't need
Node. After changing the UI, run `make ui` and commit `dist/` with it. CI rebuilds it
and fails if the committed copy doesn't match.

The fake API ([tests/fake-api.mjs](../ui/dashboard/tests/fake-api.mjs)) mirrors the
daemon's behaviour: it serves `dist/`, requires the write header, and streams events.
It also has test hooks under `/__test/` to inject events and change state.
