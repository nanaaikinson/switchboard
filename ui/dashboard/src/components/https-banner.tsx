import { useState } from "react";
import { ShieldAlert, TriangleAlert } from "lucide-react";
import type { HTTPSState } from "@/lib/api";
import { useLive } from "@/lib/live";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";

/** Explains, above the routes, why https:// links won't just work. */
export function HTTPSBanner({ state }: { state: HTTPSState }) {
  const { status, ca, refreshCA } = useLive();
  const [checking, setChecking] = useState(false);

  if (state === "down") {
    return (
      <Alert variant="destructive" data-testid="https-banner">
        <ShieldAlert />
        <AlertTitle>HTTPS isn't running</AlertTitle>
        <AlertDescription>
          {status?.https.error && <p className="font-mono text-xs break-all">{status.https.error}</p>}
          <p>
            Links below use plain http:// until it's fixed. Run <code className="font-mono">sb doctor</code> in a terminal to
            see why.
          </p>
        </AlertDescription>
      </Alert>
    );
  }
  if (state !== "untrusted") return null;

  async function check() {
    setChecking(true);
    try {
      await refreshCA();
    } finally {
      setChecking(false);
    }
  }
  return (
    <Alert variant="warning" data-testid="https-banner">
      <TriangleAlert />
      <AlertTitle>Browsers don't trust Switchboard's certificates yet</AlertTitle>
      <AlertDescription>
        <p>
          {ca?.present === false ? "There's no local CA yet. " : ""}HTTPS works, but browsers show a certificate warning for
          every route. Run <code className="font-mono">sb trust</code> in a terminal, then check again.
        </p>
        <Button variant="outline" size="sm" className="mt-2" onClick={check} disabled={checking}>
          {checking ? "Checking…" : "Check again"}
        </Button>
      </AlertDescription>
    </Alert>
  );
}
