import { Lock, LockOpen } from "lucide-react";
import { certNames, type Health, type HTTPSState, type MDNSState, type RouteStatus } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

const healthText: Record<Health, string> = {
  up: "Up: the app is accepting connections",
  down: "Down: nothing is listening on this port. Is the app running?",
  unknown: "Checking…",
};

export function StatusDot({ health }: { health: Health }) {
  return (
    <span
      role="img"
      aria-label={health}
      title={healthText[health]}
      data-health={health}
      className={cn(
        "inline-block size-2.5 rounded-full",
        health === "up" && "bg-success",
        health === "down" && "bg-destructive",
        health === "unknown" && "bg-muted-foreground/40",
      )}
    />
  );
}

export function SourceBadge({ route }: { route: RouteStatus }) {
  if (route.source === "docker") {
    return <Badge variant="secondary">docker · {route.container}</Badge>;
  }
  if (route.source === "file") {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <Badge variant="outline" className="max-w-48 cursor-default truncate">
            file · {route.file?.split("/").slice(-2).join("/")}
          </Badge>
        </TooltipTrigger>
        <TooltipContent>{route.file}</TooltipContent>
      </Tooltip>
    );
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="outline" className="cursor-default">
          manual
        </Badge>
      </TooltipTrigger>
      <TooltipContent>Added here or with sb add</TooltipContent>
    </Tooltip>
  );
}

/** Why a route can't be edited here, or null if it can. */
export function readOnlyReason(r: RouteStatus): string | null {
  switch (r.source) {
    case "docker":
      return `Managed by the Docker container ${r.container}. To change it, edit the container's labels.`;
    case "file":
      return `Managed by ${r.file}. To change it, edit that file and run sb apply.`;
    default:
      return null;
  }
}

const lockText: Record<Exclude<HTTPSState, "unknown">, string> = {
  ready: "HTTPS ready",
  untrusted: "HTTPS works, certificate not trusted",
  down: "HTTPS not running",
};

/** A lock showing whether https:// works for the route, with its certificate in the tooltip. */
export function HTTPSLock({ route, state }: { route: Pick<RouteStatus, "name" | "wildcard">; state: HTTPSState }) {
  if (state === "unknown") return <Lock className="size-3.5 text-muted-foreground/40" aria-hidden />;
  const names = certNames(route);
  const Icon = state === "down" ? LockOpen : Lock;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span role="img" aria-label={lockText[state]} data-https={state} tabIndex={0} className="inline-flex">
          <Icon
            className={cn(
              "size-3.5",
              state === "ready" && "text-success",
              state === "untrusted" && "text-warning",
              state === "down" && "text-muted-foreground",
            )}
          />
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-72">
        {state === "down" ? (
          <>HTTPS isn't running, so this route only works over plain http:// for now. Run sb doctor to see why.</>
        ) : (
          <>
            {state === "ready" ? "HTTPS with a trusted certificate" : "HTTPS works, but browsers show a warning until you run sb trust"}
            <span className="mt-1 block font-mono text-[11px] opacity-80">
              Certificate: {names.join(", ")}
            </span>
          </>
        )}
      </TooltipContent>
    </Tooltip>
  );
}

const mdnsText: Record<MDNSState, { label: string; tip: string }> = {
  announced: { label: "mDNS", tip: "Announced over multicast DNS on this machine only (experimental)." },
  pending: { label: "mDNS pending", tip: "Not announced yet. If this stays, run sb doctor to see why (experimental)." },
  wildcard: {
    label: "not on mDNS",
    tip: "Wildcards can't be announced over multicast DNS, so these names don't resolve. Add each .local name you need.",
  },
};

/** How a .local route is announced over mDNS; nothing for other routes. */
export function MDNSBadge({ route }: { route: Pick<RouteStatus, "mdns" | "wildcard"> }) {
  if (!route.mdns) return null;
  const { label, tip } = mdnsText[route.mdns];
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge
          variant={route.mdns === "announced" ? "secondary" : "outline"}
          data-mdns={route.mdns}
          tabIndex={0}
          className={cn("ml-2 cursor-default", route.mdns === "wildcard" && "text-warning")}
        >
          {label}
        </Badge>
      </TooltipTrigger>
      <TooltipContent className="max-w-64">
        {tip}
        {route.wildcard && route.mdns !== "wildcard" && " Its subdomains aren't announced."}
      </TooltipContent>
    </Tooltip>
  );
}
