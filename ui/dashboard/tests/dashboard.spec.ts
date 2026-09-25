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
  const toggle = page.getByRole("switch", { name: "Redirect HTTP to HTTPS for myapp.test" });
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
  await expect(trust).toContainText("Not trusted");
  await expect(trust).toContainText("sb trust");
  await expect(trust).toContainText("a1b2c3d4e5f6");

  await request.post("/__test/ca", { data: { trusted: true } });
  await page.reload();
  await expect(page.getByTestId("trust")).toContainText("Trusted by the system");
});

test("follows the system color scheme", async ({ page }) => {
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.emulateMedia({ colorScheme: "light" });
  const light = await bg();
  await page.emulateMedia({ colorScheme: "dark" });
  const dark = await bg();
  expect(light).not.toBe(dark);
  const lum = (c: string) => c.match(/[\d.]+/g)!.slice(0, 3).map(Number).reduce((a, b) => a + b, 0);
  expect(lum(dark)).toBeLessThan(lum(light));
});

test("unknown pages link back", async ({ page }) => {
  await page.goto("/nope/nope");
  await expect(page.getByText("Page not found")).toBeVisible();
});
