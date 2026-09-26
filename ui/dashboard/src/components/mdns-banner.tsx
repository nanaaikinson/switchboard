import { FlaskConical, TriangleAlert } from "lucide-react";
import { useLive } from "@/lib/live";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

/** Says, above the routes, that the experimental .local mode is on, and whether it works. */
export function MDNSBanner() {
  const { status } = useLive();
  const m = status?.mdns;
  if (!m?.enabled) return null;
  if (m.error) {
    return (
      <Alert variant="warning" data-testid="mdns-banner">
        <TriangleAlert />
        <AlertTitle>.local names aren't announced (experimental)</AlertTitle>
        <AlertDescription>
          <p className="font-mono text-xs break-all">{m.error}</p>
          <p>
            Switchboard retries every 10 seconds. Run <code className="font-mono">sb doctor</code> in a terminal to see why.
          </p>
        </AlertDescription>
      </Alert>
    );
  }
  return (
    <Alert data-testid="mdns-banner">
      <FlaskConical />
      <AlertTitle>.local mode is on (experimental)</AlertTitle>
      <AlertDescription>
        <p>
          {m.backend
            ? `${m.announced} ${m.announced === 1 ? "name is" : "names are"} announced over mDNS via ${m.backend}, on the ${m.interface} interface only.`
            : "Starting the mDNS announcer…"}{" "}
          Wildcard routes can't be announced. Run <code className="font-mono">sb doctor</code> to check that .local lookups
          stay on this machine.
        </p>
      </AlertDescription>
    </Alert>
  );
}
