import { expect, test, type Page } from "@playwright/test";

test.beforeEach(async ({ page, request }) => {
  await request.post("/__test/reset");
  await page.goto("/");
  await expect(page.getByTestId("connection")).toHaveText(/Live/);
});

const row = (page: Page, name: string) => page.locator(`tr[data-route="${name}"]`);

async function emit(page: Page, type: string, route: object) {
  await page.request.post("/__test/event", { data: { type, route } });
}

test("lists routes with status, source and a link to each", async ({ page }) => {
  await expect(row(page, "myapp.test").getByRole("img", { name: "up" })).toBeVisible();
  await expect(row(page, "api.myapp.test").getByRole("img", { name: "down" })).toBeVisible();
  await expect(row(page, "myapp.test").getByRole("link", { name: "myapp.test", exact: true })).toHaveAttribute("href", "https://myapp.test/");
  await expect(row(page, "api.myapp.test")).toContainText("+ *.api.myapp.test");
  await expect(row(page, "shop.test")).toContainText("file · shop/switchboard.toml");
  await expect(row(page, "web.test")).toContainText("docker · web");
  await expect(page.getByTestId("skipped")).toContainText("worker-1");
});

test("validates the add form, then adds a route", async ({ page }) => {
  const add = page.getByRole("button", { name: "Add route" });
  await page.getByLabel("Name").fill("Not Valid!");
  await page.getByLabel("Port").fill("99999");
  await add.click();
  await expect(page.getByLabel("Name")).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByText("Use letters, digits and hyphens")).toBeVisible();
  await expect(page.getByText("Enter a port from 1 to 65535.")).toBeVisible();

  await page.getByLabel("Name").fill("blog");
  await expect(page.getByText("https://blog.test")).toBeVisible();
  await page.getByLabel("Port").fill("5173");
  await add.click();
  await expect(row(page, "blog.test")).toContainText("5173");
  await expect(page.getByLabel("Name")).toHaveValue("");
});

test("shows the daemon's error when it rejects a route", async ({ page }) => {
  await page.getByLabel("Name").fill("taken");
  await page.getByLabel("Port").fill("3000");
  await page.getByRole("button", { name: "Add route" }).click();
  await expect(page.getByRole("alert")).toContainText("taken.test is reserved");
});

test("toggles the HTTPS redirect", async ({ page, request }) => {
  const toggle = page.getByRole("switch", { name: "HTTPS for myapp.test" });
  await expect(toggle).toBeChecked();
  await toggle.click();
  await expect(toggle).not.toBeChecked();
  const routes = await (await request.get("/v1/routes")).json();
  expect(routes.find((r: { name: string }) => r.name === "myapp.test").redirect_https).toBe(false);
});

test("deletes a route after confirming", async ({ page }) => {
  await row(page, "myapp.test").getByRole("button", { name: "Delete myapp.test" }).click();
  await expect(page.getByRole("alertdialog")).toContainText("Delete myapp.test?");
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(row(page, "myapp.test")).toBeVisible();

  await row(page, "myapp.test").getByRole("button", { name: "Delete myapp.test" }).click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(row(page, "myapp.test")).toHaveCount(0);
});

test("routes from files and Docker can't be edited here", async ({ page }) => {
  for (const name of ["shop.test", "web.test"]) {
    await expect(row(page, name).getByRole("switch")).toBeDisabled();
    await expect(row(page, name).getByRole("button", { name: `Delete ${name}` })).toBeDisabled();
  }
  await row(page, "shop.test").getByRole("switch").hover({ force: true });
  await expect(page.getByRole("tooltip").first()).toContainText("sb apply");
});

test("follows live events", async ({ page }) => {
  await emit(page, "health.changed", { name: "myapp.test", health: "down" });
  await expect(row(page, "myapp.test").getByRole("img", { name: "down" })).toBeVisible();

  await emit(page, "route.added", {
    name: "live.test", port: 9000, wildcard: false, redirect_https: true, health: "unknown", source: "docker", container: "live",
  });
  await expect(row(page, "live.test")).toContainText("docker · live");
});

test("shows a route's recent requests", async ({ page }) => {
  await row(page, "myapp.test").getByRole("link", { name: "Recent requests to myapp.test" }).click();
  await expect(page).toHaveURL(/\/routes\/myapp\.test$/);
  const logs = page.getByTestId("logs");
  await expect(logs).toContainText("/api/login");
  await expect(logs).toContainText("502");
  await expect(logs.locator("tbody tr").first()).toContainText("POST"); // newest first

  await page.goto("/routes/api.myapp.test");
  await expect(page.getByText("No requests yet")).toBeVisible();
});

test("settings show TLDs and trust status", async ({ page, request }) => {
  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page.getByTestId("tlds")).toHaveText(".test");
  const trust = page.getByTestId("trust");
  await expect(trust).toContainText("Trusted by the system");
  await expect(trust).toContainText("a1b2c3d4e5f6");

  await request.post("/__test/ca", { data: { trusted: false, error: "certificate is not trusted" } });
  await page.getByRole("link", { name: "Routes" }).click();
  await page.getByRole("link", { name: "Settings" }).click(); // re-checks trust
  await expect(page.getByTestId("trust")).toContainText("Not trusted");
  await expect(page.getByTestId("trust")).toContainText("sb trust");
});

test("HTTPS ready: a green lock with the certificate name, and no banner", async ({ page }) => {
  await expect(page.getByTestId("https-banner")).toHaveCount(0);
  const lock = row(page, "myapp.test").getByRole("img", { name: "HTTPS ready" });
  await expect(lock).toBeVisible();
  await lock.hover();
  await expect(page.getByRole("tooltip")).toContainText("Certificate: myapp.test");

  // A route that also matches subdomains is covered by a wildcard too.
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await row(page, "api.myapp.test").getByRole("img", { name: "HTTPS ready" }).hover();
  await expect(page.getByRole("tooltip")).toContainText("api.myapp.test, *.api.myapp.test");
});

test("untrusted CA: a warning banner, amber locks, and Check again", async ({ page, request }) => {
  await request.post("/__test/ca", { data: { trusted: false } });
  await page.reload();
  const banner = page.getByTestId("https-banner");
  await expect(banner).toContainText("Browsers don't trust Switchboard's certificates yet");
  await expect(banner).toContainText("sb trust");
  await expect(row(page, "myapp.test").getByRole("img", { name: "HTTPS works, certificate not trusted" })).toBeVisible();
  // Links still use https://: it works, with a warning.
  await expect(row(page, "myapp.test").getByRole("link", { name: "myapp.test", exact: true })).toHaveAttribute("href", "https://myapp.test/");

  // The user runs sb trust, then checks again.
  await request.post("/__test/ca", { data: { trusted: true } });
  await banner.getByRole("button", { name: "Check again" }).click();
  await expect(banner).toHaveCount(0);
  await expect(row(page, "myapp.test").getByRole("img", { name: "HTTPS ready" })).toBeVisible();
});

test("no CA at all counts as untrusted", async ({ page, request }) => {
  await request.post("/__test/ca", { data: { present: false, trusted: false } });
  await page.reload();
  await expect(page.getByTestId("https-banner")).toContainText("There's no local CA yet.");
});

test("HTTPS down: an error banner, open locks, and plain-HTTP links", async ({ page, request }) => {
  await request.post("/__test/https", {
    data: { listening: false, addrs: null, error: "proxy: listen 127.0.0.1:443: bind: address already in use" },
  });
  await page.reload();
  const banner = page.getByTestId("https-banner");
  await expect(banner).toContainText("HTTPS isn't running");
  await expect(banner).toContainText("address already in use");
  await expect(banner).toContainText("sb doctor");
  const lock = row(page, "myapp.test").getByRole("img", { name: "HTTPS not running" });
  await expect(lock).toBeVisible();
  await lock.hover();
  await expect(page.getByRole("tooltip")).toContainText("only works over plain http://");
  // The fake moves plain HTTP to :8080, so the port must be kept.
  await expect(row(page, "myapp.test").getByRole("link", { name: "myapp.test", exact: true })).toHaveAttribute("href", "http://myapp.test:8080/");
});

test("the HTTPS column explains what it does", async ({ page }) => {
  await page.getByRole("columnheader", { name: /HTTPS/ }).locator("span").first().hover();
  await expect(page.getByRole("tooltip")).toContainText("Serve this over TLS. When on, http:// requests are redirected to https://.");
  const https = page.getByRole("switch", { name: "HTTPS", exact: true });
  await expect(https).toBeChecked(); // the add form's default
  await expect(https).toHaveAccessibleDescription("Serve this over TLS");
});

test("the subdomain option names what it matches", async ({ page }) => {
  await expect(page.getByText("Also send *.myapp.test to this port")).toBeVisible();
  await page.getByLabel("Name").fill("blog");
  await expect(page.getByText("Also send *.blog.test to this port")).toBeVisible();
  await page.getByLabel("Name").fill("*.blog");
  await expect(page.getByText("This name is already a wildcard")).toBeVisible();
});

test("follows the system color scheme", async ({ page }) => {
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.emulateMedia({ colorScheme: "light" });
  const light = await bg();
  await page.emulateMedia({ colorScheme: "dark" });
  await expect.poll(bg).not.toBe(light); // the change event is async
  const dark = await bg();
  const lum = (c: string) => c.match(/[\d.]+/g)!.slice(0, 3).map(Number).reduce((a, b) => a + b, 0);
  expect(lum(dark)).toBeLessThan(lum(light));
});

test("the theme toggle overrides the system and is remembered", async ({ page }) => {
  const isDark = () => page.evaluate(() => document.documentElement.classList.contains("dark"));
  const theme = page.getByTestId("theme");
  await page.emulateMedia({ colorScheme: "light" });
  await expect(theme.getByRole("button", { name: "Match system theme" })).toHaveAttribute("aria-pressed", "true");
  expect(await isDark()).toBe(false);

  await theme.getByRole("button", { name: "Dark theme" }).click();
  await expect(theme.getByRole("button", { name: "Dark theme" })).toHaveAttribute("aria-pressed", "true");
  expect(await isDark()).toBe(true);
  expect(await page.evaluate(() => document.documentElement.style.colorScheme)).toBe("dark");

  // public/theme.js applies it before the app loads, so there's no flash.
  await page.reload();
  expect(await isDark()).toBe(true);
  await expect(theme.getByRole("button", { name: "Dark theme" })).toHaveAttribute("aria-pressed", "true");

  await theme.getByRole("button", { name: "Light theme" }).click();
  await page.emulateMedia({ colorScheme: "dark" });
  await page.waitForTimeout(100); // give a (wrong) change handler time to run
  expect(await isDark()).toBe(false); // a choice beats the system

  await theme.getByRole("button", { name: "Match system theme" }).click();
  await expect.poll(isDark).toBe(true);
  await page.emulateMedia({ colorScheme: "light" });
  await expect.poll(isDark).toBe(false);
  expect(await page.evaluate(() => localStorage.getItem("switchboard-theme"))).toBeNull();
});

test("unknown pages link back", async ({ page }) => {
  await page.goto("/nope/nope");
  await expect(page.getByText("Page not found")).toBeVisible();
});
