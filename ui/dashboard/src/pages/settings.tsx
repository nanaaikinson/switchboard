import { useEffect, type ReactNode } from "react";
import type { Listener } from "@/lib/api";
import { useLive } from "@/lib/live";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export function SettingsPage() {
  const { status, ca, refreshCA } = useLive();

  useEffect(() => {
    void refreshCA(); // trust may have changed since the app loaded
  }, [refreshCA]);

  return (
    <div className="grid gap-6 md:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>Names</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          <Row label="TLDs">
            <span className="flex flex-wrap gap-1" data-testid="tlds">
              {status?.tlds.map((t) => (
                <Badge key={t} variant="secondary">
                  .{t}
                </Badge>
              ))}
            </span>
          </Row>
          <p className="text-muted-foreground">
            Names that don't end in one of these get .{status?.tlds[0] ?? "test"} added. You can't change TLDs from the
            dashboard yet.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Certificates</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 text-sm" data-testid="trust">
          {!ca ? (
            <p className="text-muted-foreground">Loading…</p>
          ) : !ca.present ? (
            <p>
              There's no local CA yet. Run <code className="font-mono">sb trust</code> in a terminal to create and trust one.
            </p>
          ) : (
            <>
              <Row label="Trust">
                {ca.trusted ? (
                  <Badge className="bg-success text-white">Trusted by the system</Badge>
                ) : (
                  <Badge variant="destructive">Not trusted</Badge>
                )}
              </Row>
              {!ca.trusted && (
                <p className="text-muted-foreground">
                  Browsers show a warning for Switchboard's certificates. Run <code className="font-mono">sb trust</code> in
                  a terminal to fix it{ca.error ? ` (${ca.error})` : ""}.
                </p>
              )}
              <Row label="Valid for">{ca.tlds?.map((t) => "." + t).join(", ")} names only</Row>
              <Row label="Expires">{ca.not_after && new Date(ca.not_after).toLocaleDateString()}</Row>
              <Row label="Fingerprint">
                <code className="break-all font-mono text-xs">{ca.fingerprint}</code>
              </Row>
            </>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Daemon</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          <Row label="Version">{status?.version}</Row>
          <Row label="Uptime">{status && formatUptime(status.uptime_seconds)}</Row>
          <ListenerRow label="DNS" l={status?.dns} />
          <ListenerRow label="HTTP" l={status?.proxy} />
          <ListenerRow label="HTTPS" l={status?.https} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Docker</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          {!status?.docker.enabled ? (
            <p className="text-muted-foreground">
              Container discovery is off, because the daemon runs with <code className="font-mono">--docker=false</code>.
            </p>
          ) : status.docker.connected ? (
            <>
              <Row label="Status">
                <Badge className="bg-success text-white">Connected</Badge>
              </Row>
              <Row label="Endpoint">
                <code className="font-mono text-xs">{status.docker.endpoint}</code>
              </Row>
            </>
          ) : (
            <>
              <Row label="Status">
                <Badge variant="secondary">Not connected</Badge>
              </Row>
              <p className="text-muted-foreground">
                Can't reach Docker. Switchboard tries again every 10 seconds, so starting Docker is enough.
              </p>
              {status.docker.error && <p className="font-mono text-xs break-all text-muted-foreground">{status.docker.error}</p>}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6rem_1fr] items-baseline gap-3">
      <span className="text-muted-foreground">{label}</span>
      <span>{children}</span>
    </div>
  );
}

function ListenerRow({ label, l }: { label: string; l: Listener | undefined }) {
  return (
    <Row label={label}>
      {!l ? null : l.listening ? (
        <span className="font-mono text-xs">{l.addrs?.join(", ")}</span>
      ) : (
        <span className="text-destructive">Not running: {l.error}</span>
      )}
    </Row>
  );
}

function formatUptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m ${s % 60}s`;
}
