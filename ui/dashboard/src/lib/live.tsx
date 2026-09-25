import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api, type CAInfo, type RouteEvent, type RouteStatus, type Status } from "./api";

export type Connection = "connecting" | "live" | "reconnecting";

interface Live {
  status: Status | null;
  routes: RouteStatus[] | null;
  /** The local CA and whether the system trusts it; null until loaded. */
  ca: CAInfo | null;
  connection: Connection;
  error: string | null;
  refresh: () => Promise<void>;
  /** Re-checks CA trust, e.g. after the user runs sb trust. */
  refreshCA: () => Promise<void>;
}

const LiveContext = createContext<Live | null>(null);

/**
 * LiveProvider loads the daemon status once, then keeps routes current from
 * /v1/events: route changes reload the list, health changes patch it in
 * place. EventSource reconnects by itself; after a reconnect everything is
 * reloaded, since events may have been missed.
 */
export function LiveProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status | null>(null);
  const [routes, setRoutes] = useState<RouteStatus[] | null>(null);
  const [ca, setCA] = useState<CAInfo | null>(null);
  const [connection, setConnection] = useState<Connection>("connecting");
  const [error, setError] = useState<string | null>(null);
  const reload = useRef<number | undefined>(undefined);

  const refresh = useCallback(async () => {
    try {
      const st = await api.status();
      setStatus(st);
      setRoutes(st.routes);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  const refreshCA = useCallback(async () => {
    try {
      setCA(await api.ca());
    } catch {
      /* the status error already explains it */
    }
  }, []);

  useEffect(() => {
    void refresh();
    void refreshCA();
    const es = new EventSource("/v1/events");
    let dropped = false;
    es.onopen = () => {
      setConnection("live");
      if (dropped) {
        void refresh();
        void refreshCA();
      }
    };
    es.onerror = () => {
      dropped = true;
      setConnection("reconnecting");
    };
    const onRouteChange = () => {
      // Coalesce bursts, e.g. several containers starting at once.
      window.clearTimeout(reload.current);
      reload.current = window.setTimeout(() => void refresh(), 100);
    };
    const onHealth = (msg: MessageEvent<string>) => {
      const ev = JSON.parse(msg.data) as RouteEvent;
      setRoutes((rs) => rs?.map((r) => (r.name === ev.route.name ? { ...r, health: ev.route.health } : r)) ?? rs);
    };
    for (const t of ["route.added", "route.updated", "route.removed"]) es.addEventListener(t, onRouteChange);
    es.addEventListener("health.changed", onHealth);
    return () => {
      es.close();
      window.clearTimeout(reload.current);
    };
  }, [refresh, refreshCA]);

  const value = useMemo(
    () => ({ status, routes, ca, connection, error, refresh, refreshCA }),
    [status, routes, ca, connection, error, refresh, refreshCA],
  );
  return <LiveContext.Provider value={value}>{children}</LiveContext.Provider>;
}

export function useLive(): Live {
  const v = useContext(LiveContext);
  if (!v) throw new Error("useLive outside LiveProvider");
  return v;
}
