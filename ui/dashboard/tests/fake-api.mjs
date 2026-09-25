// A fake Switchboard control API for the Playwright tests (and `npm run dev`).
// It serves dist/ like the daemon does, keeps routes in memory, streams
// events on /v1/events, and requires the same write header as the daemon.
// Test hooks live under /__test/.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";

const PORT = Number(process.env.PORT ?? 5199);
const dist = fileURLToPath(new URL("../dist/", import.meta.url));

function initialState() {
  return {
    routes: [
      { name: "myapp.test", port: 3000, wildcard: false, redirect_https: true, health: "up", source: "config" },
      { name: "api.myapp.test", port: 3001, wildcard: true, redirect_https: false, health: "down", source: "config" },
      { name: "shop.test", port: 4000, wildcard: false, redirect_https: true, health: "up", source: "file", file: "/home/me/shop/switchboard.toml" },
      { name: "web.test", port: 8080, wildcard: false, redirect_https: true, health: "unknown", source: "docker", container: "web" },
    ],
    logs: {
      "myapp.test": [
        { time: "2026-09-25T10:00:00Z", host: "myapp.test", method: "GET", path: "/", status: 200, duration_ms: 12.5 },
        { time: "2026-09-25T10:00:01Z", host: "myapp.test", method: "POST", path: "/api/login", status: 502, duration_ms: 0.4 },
      ],
    },
    ca: {
      present: true,
      fingerprint: "a1b2c3d4e5f6",
      not_after: "2036-09-22T00:00:00Z",
      tlds: ["test"],
      trusted: true,
    },
    https: { addrs: ["127.0.0.1:443", "[::1]:443"], listening: true },
    proxy: { addrs: ["127.0.0.1:80", "[::1]:80"], listening: true },
  };
}

let state = initialState();
const clients = new Set();

function status() {
  return {
    version: "v0.9.0-fake",
    uptime_seconds: 3725,
    tlds: ["test"],
    dns: { addrs: ["127.0.0.1:15353"], listening: true },
    proxy: state.proxy,
    https: state.https,
    docker: {
      enabled: true,
      connected: true,
      endpoint: "unix:///var/run/docker.sock",
      skipped: [{ container: "worker-1", reason: "it publishes 2 ports (5001->5000, 6001->6000) and none is container port 80, 8080 or 3000; set dev.switchboard.port" }],
    },
    routes: state.routes,
  };
}

function publish(type, route) {
  const msg = `event: ${type}\ndata: ${JSON.stringify({ type, route })}\n\n`;
  for (const res of clients) res.write(msg);
}

function send(res, code, body) {
  res.writeHead(code, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

async function body(req) {
  let data = "";
  for await (const chunk of req) data += chunk;
  return data ? JSON.parse(data) : {};
}

const hostname = /^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/;
const qualify = (n) => (n.endsWith(".test") ? n : n + ".test");

async function handleAPI(req, res, url) {
  const path = url.pathname;
  if (req.method !== "GET" && req.headers["x-requested-with"] !== "switchboard") {
    return send(res, 403, { error: "cross-site request refused" });
  }
  if (req.method === "GET" && path === "/v1/status") return send(res, 200, status());
  if (req.method === "GET" && path === "/v1/routes") return send(res, 200, state.routes);
  if (req.method === "GET" && path === "/v1/ca") return send(res, 200, state.ca);
  if (req.method === "GET" && path === "/v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
    res.write(": connected\n\n");
    clients.add(res);
    req.on("close", () => clients.delete(res));
    return;
  }
  let m = path.match(/^\/v1\/routes\/([^/]+)\/logs$/);
  if (req.method === "GET" && m) {
    const name = qualify(decodeURIComponent(m[1]));
    if (!state.routes.some((r) => r.name === name)) return send(res, 404, { error: `route not found: ${name}` });
    return send(res, 200, state.logs[name] ?? []);
  }
  if (req.method === "POST" && path === "/v1/routes") {
    const r = await body(req);
    const name = qualify(String(r.name ?? "").toLowerCase());
    if (!hostname.test(name)) return send(res, 400, { error: `invalid route: name "${name}" must be a hostname like myapp or *.myapp` });
    if (!(r.port >= 1 && r.port <= 65535)) return send(res, 400, { error: `invalid route: port ${r.port} out of range 1-65535` });
    if (name === "taken.test") return send(res, 400, { error: "invalid route: taken.test is reserved for testing" });
    const route = { name, port: r.port, wildcard: !!r.wildcard, redirect_https: !!r.redirect_https, health: "unknown", source: "config" };
    const i = state.routes.findIndex((x) => x.name === name);
    if (i >= 0) state.routes[i] = { ...state.routes[i], ...route, health: state.routes[i].health };
    else state.routes.push(route);
    publish(i >= 0 ? "route.updated" : "route.added", route);
    return send(res, i >= 0 ? 200 : 201, route);
  }
  m = path.match(/^\/v1\/routes\/([^/]+)$/);
  if (req.method === "DELETE" && m) {
    const name = qualify(decodeURIComponent(m[1]));
    const i = state.routes.findIndex((x) => x.name === name);
    if (i < 0) return send(res, 404, { error: `route not found: ${name}` });
    if (state.routes[i].source !== "config") return send(res, 409, { error: `conflict: ${name} comes from somewhere else` });
    const [gone] = state.routes.splice(i, 1);
    publish("route.removed", gone);
    return send(res, 200, gone);
  }
  return send(res, 404, { error: "not found" });
}

async function handleTest(req, res, url) {
  if (url.pathname === "/__test/reset") {
    state = initialState();
    return send(res, 200, {});
  }
  if (url.pathname === "/__test/event") {
    // { type, route } is published as is; health.changed also updates state.
    const ev = await body(req);
    if (ev.type === "health.changed") {
      const r = state.routes.find((x) => x.name === ev.route.name);
      if (r) r.health = ev.route.health;
    }
    if (ev.type === "route.added") state.routes.push(ev.route);
    publish(ev.type, ev.route);
    return send(res, 200, {});
  }
  if (url.pathname === "/__test/https") {
    // e.g. { listening: false, error: "..." }; HTTP moves to :8080 to test port suffixes.
    const b = await body(req);
    state.https = { ...state.https, ...b };
    if (b.listening === false) state.proxy = { addrs: ["127.0.0.1:8080"], listening: true };
    return send(res, 200, {});
  }
  if (url.pathname === "/__test/ca") {
    state.ca = { ...state.ca, ...(await body(req)) };
    return send(res, 200, {});
  }
  return send(res, 404, {});
}

const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" };

async function handleStatic(res, url) {
  // Like the daemon: missing files under /assets 404; any other path is a
  // client-side route (route names like /routes/myapp.test look like files).
  let file = normalize(url.pathname).replace(/^(\.\.[/\\])+/, "");
  let data;
  try {
    data = await readFile(join(dist, file === "/" ? "/index.html" : file));
  } catch {
    if (file.startsWith("/assets/")) return res.writeHead(404).end("not found; run npm run build");
    file = "/index.html";
    data = await readFile(join(dist, file));
  }
  res.writeHead(200, { "Content-Type": types[extname(file)] ?? "text/html" });
  res.end(data);
}

createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  try {
    if (url.pathname.startsWith("/v1/")) return await handleAPI(req, res, url);
    if (url.pathname.startsWith("/__test/")) return await handleTest(req, res, url);
    return await handleStatic(res, url);
  } catch (e) {
    send(res, 500, { error: String(e) });
  }
}).listen(PORT, "127.0.0.1", () => console.log(`fake API on http://127.0.0.1:${PORT}`));
