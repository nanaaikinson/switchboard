import { createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { Layout } from "@/components/layout";
import { RoutesPage } from "@/pages/routes";
import { RouteLogsPage } from "@/pages/route-logs";
import { SettingsPage } from "@/pages/settings";
import { NotFound } from "@/pages/not-found";

const rootRoute = createRootRoute({ component: Layout, notFoundComponent: NotFound });

const routesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: RoutesPage });

export const routeLogsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/routes/$name",
  component: RouteLogsPage,
});

const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/settings", component: SettingsPage });

export const router = createRouter({
  routeTree: rootRoute.addChildren([routesRoute, routeLogsRoute, settingsRoute]),
  defaultPreload: "intent",
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
