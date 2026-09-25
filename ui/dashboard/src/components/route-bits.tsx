import { Lock, LockOpen } from "lucide-react";
import { certNames, type Health, type HTTPSState, type RouteStatus } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

const healthText: Record<Health, string> = {
  up: "Up: the app is accepting connections",
  down: "Down: nothing is listening on the port",
  unknown: "Not checked yet",
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
  return <Badge variant="outline">sb add</Badge>;
}

/** Why a route can't be edited here, or null if it can. */
export function readOnlyReason(r: RouteStatus): string | null {
  switch (r.source) {
    case "docker":
      return `Comes from Docker container ${r.container}; change its labels instead.`;
    case "file":
      return `Comes from ${r.file}; edit that file and run sb apply.`;
    default:
      return null;
  }
}

const lockText: Record<Exclude<HTTPSState, "unknown">, string> = {
  ready: "HTTPS ready",
  untrusted: "HTTPS works, certificate not trusted",
  down: "HTTPS unavailable",
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
          <>HTTPS isn't running, so this route only works over plain HTTP. Run sb doctor to see why.</>
        ) : (
          <>
            {state === "ready" ? "HTTPS with a trusted certificate" : "HTTPS works, but browsers will warn until you run sb trust"}
            <span className="mt-1 block font-mono text-[11px] opacity-80">
              Certificate: {names.join(", ")}
            </span>
          </>
        )}
      </TooltipContent>
    </Tooltip>
  );
}
