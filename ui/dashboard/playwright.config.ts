import { defineConfig, devices } from "@playwright/test";

// Smoke tests of the built dashboard against tests/fake-api.mjs. Run with
// `npm test`, which builds first.
export default defineConfig({
  testDir: "tests",
  fullyParallel: false, // one fake API with shared state
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: { baseURL: "http://127.0.0.1:5199", trace: "retain-on-failure" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "node tests/fake-api.mjs",
    url: "http://127.0.0.1:5199/v1/status",
    reuseExistingServer: !process.env.CI,
  },
});
