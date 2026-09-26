import { useState, type FormEvent, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { ExternalLink, Info, ScrollText, Trash2 } from "lucide-react";
import { api, httpsState, qualifyName, routeURL, validName, validPort, type HTTPSState, type RouteStatus } from "@/lib/api";
import { useLive } from "@/lib/live";
import { HTTPSBanner } from "@/components/https-banner";
import { MDNSBanner } from "@/components/mdns-banner";
import { HTTPSLock, MDNSBadge, readOnlyReason, SourceBadge, StatusDot } from "@/components/route-bits";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

export function RoutesPage() {
  const { routes, status, ca, refresh } = useLive();
  const tlds = status?.tlds.length ? status.tlds : ["test"];
  const https = httpsState(status, ca);
  return (
    <div className="space-y-6">
      <HTTPSBanner state={https} />
      <MDNSBanner />
      <AddRouteForm tlds={tlds} onAdded={refresh} />
      <Card>
        <CardHeader>
          <CardTitle>Routes</CardTitle>
        </CardHeader>
        <CardContent>
          {routes === null ? (
            <div className="space-y-2">
              <Skeleton className="h-8 w-full" />
              <Skeleton className="h-8 w-full" />
            </div>
          ) : routes.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">
              No routes yet. Add one above, or run <code className="font-mono">sb add myapp 3000</code>.
            </p>
          ) : (
            <RoutesTable routes={routes} https={https} onChange={refresh} />
          )}
        </CardContent>
      </Card>
      {!!status?.docker.skipped?.length && (
        <Card>
          <CardHeader>
            <CardTitle>Containers without a route</CardTitle>
            <p className="text-sm text-muted-foreground">Docker containers Switchboard found but couldn't route, and why.</p>
          </CardHeader>
          <CardContent>
            <ul className="space-y-1 text-sm" data-testid="skipped">
              {status.docker.skipped.map((s) => (
                <li key={s.container + s.reason}>
                  <span className="font-medium">{s.container}</span>: <span className="text-muted-foreground">{s.reason}</span>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function AddRouteForm({ tlds, onAdded }: { tlds: string[]; onAdded: () => Promise<void> }) {
  const tld = tlds[0];
  const [name, setName] = useState("");
  const [port, setPort] = useState("");
  const [redirect, setRedirect] = useState(true);
  const [wildcard, setWildcard] = useState(false);
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [serverError, setServerError] = useState<string | null>(null);

  const cleanName = name.trim().toLowerCase().replace(/\.$/, "");
  const nameError = !validName(cleanName) ? "Use letters, digits and hyphens, like myapp or api.myapp. Start with *. to match every subdomain." : null;
  const portError = !validPort(port.trim()) ? "Enter a port from 1 to 65535." : null;
  const qualified = qualifyName(cleanName, tlds);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    setServerError(null);
    if (nameError || portError) return;
    setBusy(true);
    try {
      await api.put({ name: cleanName, port: Number(port), redirect_https: redirect, wildcard });
      setName("");
      setPort("");
      setWildcard(false);
      setRedirect(true);
      setTouched(false);
      await onAdded();
    } catch (err) {
      setServerError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a route</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} noValidate className="grid gap-4 sm:grid-cols-[1fr_8rem_auto] sm:items-start">
          <div className="grid gap-1.5">
            <Label htmlFor="name">Name</Label>
            <Input
              id="name"
              placeholder="myapp"
              autoComplete="off"
              spellCheck={false}
              value={name}
              onChange={(e) => setName(e.target.value)}
              aria-invalid={touched && !!nameError}
              aria-describedby="name-help"
            />
            <p id="name-help" className={touched && nameError ? "text-xs text-destructive" : "text-xs text-muted-foreground"}>
              {touched && nameError ? nameError : qualified ? `https://${qualified}` : `.${tld} is added if you leave it off.`}
            </p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="port">Port</Label>
            <Input
              id="port"
              inputMode="numeric"
              placeholder="3000"
              value={port}
              onChange={(e) => setPort(e.target.value)}
              aria-invalid={touched && !!portError}
              aria-describedby="port-help"
            />
            <p id="port-help" className="text-xs text-destructive">
              {touched && portError}
            </p>
          </div>
          <div className="flex items-end gap-2 sm:pt-5.5">
            <Button type="submit" disabled={busy}>
              {busy ? "Adding…" : "Add route"}
            </Button>
          </div>
          <div className="flex flex-wrap gap-x-8 gap-y-4 sm:col-span-3">
            <label className="flex items-start gap-3 text-sm">
              <Switch checked={redirect} onCheckedChange={setRedirect} aria-label="HTTPS" aria-describedby="https-help" className="mt-0.5" />
              <span className="grid gap-0.5">
                <span className="font-medium">HTTPS</span>
                <span id="https-help" className="text-xs text-muted-foreground">
                  Serve this over TLS
                </span>
              </span>
            </label>
            <label className="flex items-start gap-3 text-sm">
              <Checkbox
                checked={wildcard}
                onCheckedChange={(v) => setWildcard(v === true)}
                aria-label="Subdomains"
                aria-describedby="wildcard-help"
                className="mt-0.5"
              />
              <span className="grid gap-0.5">
                <span className="font-medium">Subdomains</span>
                <span id="wildcard-help" className="text-xs text-muted-foreground">
                  {qualified.startsWith("*.")
                    ? "This name is already a wildcard"
                    : `Also send *.${qualified || `myapp.${tld}`} to this port`}
                </span>
              </span>
            </label>
          </div>
          {serverError && (
            <p role="alert" className="text-sm text-destructive sm:col-span-3">
              {serverError}
            </p>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

function RoutesTable({ routes, https, onChange }: { routes: RouteStatus[]; https: HTTPSState; onChange: () => Promise<void> }) {
  const { status } = useLive();
  const [error, setError] = useState<string | null>(null);

  async function run(action: () => Promise<unknown>) {
    setError(null);
    try {
      await action();
      await onChange();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <>
      {error && (
        <p role="alert" className="mb-3 text-sm text-destructive">
          {error}
        </p>
      )}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-6">
              <span className="sr-only">Status</span>
            </TableHead>
            <TableHead>Name</TableHead>
            <TableHead className="w-20">Port</TableHead>
            <TableHead className="hidden md:table-cell">Source</TableHead>
            <TableHead className="w-24">
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="inline-flex cursor-default items-center gap-1" tabIndex={0}>
                    HTTPS <Info className="size-3.5 text-muted-foreground" />
                  </span>
                </TooltipTrigger>
                <TooltipContent className="max-w-64">
                  Serve this over TLS. When on, http:// requests are redirected to https://. When off, plain http:// works
                  too.
                </TooltipContent>
              </Tooltip>
            </TableHead>
            <TableHead className="w-24 text-right">
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {routes.map((r) => {
            const url = routeURL(r, status);
            const readOnly = readOnlyReason(r);
            return (
              <TableRow key={r.name} data-route={r.name}>
                <TableCell>
                  <StatusDot health={r.health} />
                </TableCell>
                <TableCell className="font-medium">
                  <span className="mr-1.5 inline-flex align-[-2px]">
                    <HTTPSLock route={r} state={https} />
                  </span>
                  {url ? (
                    <a href={url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 hover:underline">
                      {r.name}
                      <ExternalLink className="size-3 text-muted-foreground" />
                    </a>
                  ) : (
                    <span>{r.name}</span>
                  )}
                  {r.wildcard && <span className="ml-2 hidden text-xs text-muted-foreground sm:inline">+ *.{r.name}</span>}
                  <MDNSBadge route={r} />
                </TableCell>
                <TableCell className="font-mono text-sm">{r.port}</TableCell>
                <TableCell className="hidden md:table-cell">
                  <SourceBadge route={r} />
                </TableCell>
                <TableCell>
                  <ReadOnlyTip reason={readOnly}>
                    <Switch
                      checked={r.redirect_https}
                      disabled={!!readOnly}
                      aria-label={`HTTPS for ${r.name}`}
                      onCheckedChange={(on) => run(() => api.put({ ...r, redirect_https: on }))}
                    />
                  </ReadOnlyTip>
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex justify-end gap-1">
                    <Button variant="ghost" size="icon" asChild>
                      <Link to="/routes/$name" params={{ name: r.name }} aria-label={`Recent requests to ${r.name}`}>
                        <ScrollText />
                      </Link>
                    </Button>
                    <ReadOnlyTip reason={readOnly}>
                      <DeleteButton route={r} disabled={!!readOnly} onConfirm={() => run(() => api.remove(r.name))} />
                    </ReadOnlyTip>
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </>
  );
}

/** Explains, on hover, why a disabled control can't be used. */
function ReadOnlyTip({ reason, children }: { reason: string | null; children: ReactNode }) {
  if (!reason) return <>{children}</>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex" tabIndex={0}>
          {children}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-64">{reason}</TooltipContent>
    </Tooltip>
  );
}

function DeleteButton({ route, disabled, onConfirm }: { route: RouteStatus; disabled: boolean; onConfirm: () => void }) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="ghost" size="icon" disabled={disabled} aria-label={`Delete ${route.name}`}>
          <Trash2 />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {route.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            {route.name} will stop working. The app on port {route.port} keeps running, and you can add the route again
            any time.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm} className="bg-destructive text-white hover:bg-destructive/90">
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
