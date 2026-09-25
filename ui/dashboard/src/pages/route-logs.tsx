import { useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { api, routeURL, type AccessLog } from "@/lib/api";
import { useLive } from "@/lib/live";
import { routeLogsRoute } from "@/router";
import { SourceBadge, StatusDot } from "@/components/route-bits";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

const POLL_MS = 2000;

export function RouteLogsPage() {
  const { name } = routeLogsRoute.useParams();
  const { routes, status } = useLive();
  const route = routes?.find((r) => r.name === name);
  const [logs, setLogs] = useState<AccessLog[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let stop = false;
    async function load() {
      try {
        const l = await api.logs(name);
        if (!stop) {
          setLogs(l);
          setError(null);
        }
      } catch (e) {
        if (!stop) setError(e instanceof Error ? e.message : String(e));
      }
    }
    void load();
    const t = window.setInterval(load, POLL_MS);
    return () => {
      stop = true;
      window.clearInterval(t);
    };
  }, [name]);

  const url = route ? routeURL(route, status) : null;
  return (
    <div className="space-y-4">
      <Link to="/" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" /> Routes
      </Link>
      <Card>
        <CardHeader className="flex flex-row flex-wrap items-center gap-3">
          {route && <StatusDot health={route.health} />}
          <CardTitle className="text-lg">
            {url ? (
              <a href={url} target="_blank" rel="noreferrer" className="hover:underline">
                {name}
              </a>
            ) : (
              name
            )}
          </CardTitle>
          {route && (
            <>
              <span className="font-mono text-sm text-muted-foreground">→ 127.0.0.1:{route.port}</span>
              <SourceBadge route={route} />
            </>
          )}
        </CardHeader>
        <CardContent>
          <h2 className="mb-3 text-sm font-medium">Recent requests</h2>
          {error ? (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          ) : logs === null ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : logs.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">No requests yet. Open the app and they'll show up here.</p>
          ) : (
            <Table data-testid="logs">
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">Time</TableHead>
                  <TableHead className="w-20">Method</TableHead>
                  <TableHead>Path</TableHead>
                  <TableHead className="w-20">Status</TableHead>
                  <TableHead className="w-24 text-right">Duration</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody className="font-mono text-xs">
                {[...logs].reverse().map((l, i) => (
                  <TableRow key={l.time + i}>
                    <TableCell className="text-muted-foreground">{new Date(l.time).toLocaleTimeString()}</TableCell>
                    <TableCell>{l.method}</TableCell>
                    <TableCell className="max-w-96 truncate" title={l.host + l.path}>
                      {l.host !== name && <span className="text-muted-foreground">{l.host}</span>}
                      {l.path}
                    </TableCell>
                    <TableCell className={cn(l.status >= 500 ? "text-destructive" : l.status >= 400 ? "text-warning" : "")}>
                      {l.status || "—"}
                    </TableCell>
                    <TableCell className="text-right text-muted-foreground">{formatDuration(l.duration_ms)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          <p className="mt-3 text-xs text-muted-foreground">
            The last 100 requests, kept in memory by the daemon. Query strings are never stored.
          </p>
        </CardContent>
      </Card>
    </div>
  );
}

function formatDuration(ms: number): string {
  if (ms < 1) return "<1 ms";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}
