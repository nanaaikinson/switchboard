import { Link, Outlet } from "@tanstack/react-router";
import { useLive } from "@/lib/live";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "@/components/theme-toggle";

const navLink =
  "rounded-md px-3 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground [&.active]:bg-secondary [&.active]:text-foreground";

export function Layout() {
  const { connection, status, error } = useLive();
  return (
    <div className="mx-auto flex min-h-svh max-w-5xl flex-col px-4 sm:px-6">
      <header className="flex flex-wrap items-center gap-x-6 gap-y-3 border-b py-4">
        <Link to="/" className="flex items-center gap-2 font-semibold tracking-tight">
          <img src="/favicon.svg" alt="" className="size-6" />
          Switchboard
        </Link>
        <nav className="flex gap-1">
          <Link to="/" className={navLink} activeOptions={{ exact: true }}>
            Routes
          </Link>
          <Link to="/settings" className={navLink}>
            Settings
          </Link>
        </nav>
        <div className="ml-auto flex items-center gap-4">
        <div className="flex items-center gap-2 text-xs text-muted-foreground" data-testid="connection">
          <span
            className={cn(
              "size-2 rounded-full",
              connection === "live" ? "bg-success" : connection === "reconnecting" ? "bg-warning" : "bg-muted-foreground",
            )}
          />
          {connection === "live" ? "Live" : connection === "reconnecting" ? "Reconnecting…" : "Connecting…"}
          {status && <span className="hidden sm:inline">· {status.version}</span>}
        </div>
        <ThemeToggle />
        </div>
      </header>
      {error && (
        <div role="alert" className="mt-4 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm">
          <p className="font-medium">Can't reach Switchboard</p>
          <p className="mt-1 text-muted-foreground">
            The daemon may have stopped. Run <code className="font-mono">sb doctor</code> in a terminal to see why. This page
            reconnects on its own. <span className="font-mono text-xs break-all">({error})</span>
          </p>
        </div>
      )}
      <main className="flex-1 py-6">
        <Outlet />
      </main>
    </div>
  );
}
