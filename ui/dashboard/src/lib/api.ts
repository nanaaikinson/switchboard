// Typed client for the Switchboard control API, as the daemon exposes it to
// the dashboard (see internal/dashboard). Same origin; the session cookie is
// sent automatically.

export type Health = "up" | "down" | "unknown";
export type Source = "config" | "file" | "docker";

export interface Route {
  name: string;
  port: number;
  wildcard: boolean;
  redirect_https: boolean;
  file?: string;
}

export interface RouteStatus extends Route {
  health: Health;
  source: Source;
  container?: string;
}

export interface Listener {
  addrs: string[] | null;
  listening: boolean;
  error?: string;
}

export interface DockerStatus {
  enabled: boolean;
  connected: boolean;
  endpoint?: string;
  error?: string;
  skipped?: { container: string; reason: string }[];
}

export interface Status {
  version: string;
  uptime_seconds: number;
  tlds: string[];
  dns: Listener;
  proxy: Listener;
  https: Listener;
  docker: DockerStatus;
  routes: RouteStatus[];
}

export interface CAInfo {
  present: boolean;
  fingerprint?: string;
  not_after?: string;
  tlds?: string[];
  trusted: boolean;
  error?: string;
}

export interface AccessLog {
  time: string;
  host: string;
  method: string;
  path: string;
  status: number;
  duration_ms: number;
}

export interface RouteEvent {
  type: "route.added" | "route.updated" | "route.removed" | "health.changed";
  route: RouteStatus;
}

export class APIError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (method !== "GET") headers["X-Requested-With"] = "switchboard"; // required for writes
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const e = (await res.json()) as { error?: string };
      if (e.error) message = e.error;
    } catch {
      /* not JSON */
    }
    throw new APIError(res.status, message);
  }
  return (await res.json()) as T;
}

export const api = {
  status: () => call<Status>("GET", "/v1/status"),
  routes: () => call<RouteStatus[]>("GET", "/v1/routes"),
  put: (r: Route) =>
    call<RouteStatus>("POST", "/v1/routes", {
      name: r.name,
      port: r.port,
      wildcard: r.wildcard,
      redirect_https: r.redirect_https,
    }),
  remove: (name: string) => call<Route>("DELETE", `/v1/routes/${encodeURIComponent(name)}`),
  logs: (name: string) => call<AccessLog[]>("GET", `/v1/routes/${encodeURIComponent(name)}/logs`),
  ca: () => call<CAInfo>("GET", "/v1/ca"),
};

/**
 * The URL a route opens at, or null for a pure wildcard like *.x.test. It is
 * https:// while the HTTPS proxy is listening, else plain http://.
 */
export function routeURL(r: Pick<Route, "name">, status: Pick<Status, "https" | "proxy"> | null | undefined): string | null {
  if (r.name.startsWith("*.")) return null;
  const secure = status?.https.listening ?? true;
  const l = secure ? status?.https : status?.proxy;
  const defaultPort = secure ? "443" : "80";
  let port = "";
  const addr = l?.addrs?.[0];
  if (addr) {
    const p = addr.slice(addr.lastIndexOf(":") + 1);
    if (p !== defaultPort) port = ":" + p;
  }
  return `${secure ? "https" : "http"}://${r.name}${port}/`;
}

/**
 * The certificate names that cover a route: its own name, and for routes that
 * also match subdomains, a wildcard (issued per subdomain level).
 */
export function certNames(r: Pick<Route, "name" | "wildcard">): string[] {
  if (r.name.startsWith("*.")) return [r.name];
  return r.wildcard ? [r.name, `*.${r.name}`] : [r.name];
}

/** How well HTTPS works for routes right now. */
export type HTTPSState = "unknown" | "ready" | "untrusted" | "down";

export function httpsState(status: Status | null, ca: CAInfo | null): HTTPSState {
  if (!status) return "unknown";
  if (!status.https.listening) return "down";
  if (!ca) return "unknown";
  return ca.present && ca.trusted ? "ready" : "untrusted";
}

const label = String.raw`[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`;
const hostname = new RegExp(String.raw`^(\*\.)?${label}(\.${label})*$`);

/** Mirrors config.ValidHostname: LDH labels, optionally prefixed by "*.". */
export function validName(name: string): boolean {
  return name.length > 0 && name.length <= 253 && hostname.test(name);
}

export function validPort(port: string): boolean {
  if (!/^\d+$/.test(port)) return false;
  const n = Number(port);
  return n >= 1 && n <= 65535;
}
