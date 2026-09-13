import { defineConfig, devices } from "@playwright/test";

// Runs e2e-stack/ against a real openlog stack (embedded UI + API), e.g. the `make e2e` compose
// project with E2E_UI=1 (test/e2e/ui_test.go sets the E2E_* variables). No dev server, no mocks.
export default defineConfig({
  testDir: "./e2e-stack",
  timeout: 180_000,
  expect: { timeout: 45_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: "list",
  outputDir: process.env.E2E_UI_OUTPUT ?? "test-results-stack",
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://127.0.0.1:18080",
    locale: "en-US",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
