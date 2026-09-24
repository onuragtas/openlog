import { defineConfig, devices } from "@playwright/test";

const PORT = Number(process.env.SHOTS_PORT ?? 5175);

// Screenshot tour of every menu and tab, against the Vite dev server with MSW mocks (no backend).
export default defineConfig({
  testDir: "./shots",
  timeout: 120_000,
  fullyParallel: false,
  workers: 1,
  reporter: "list",
  use: {
    baseURL: `http://localhost:${PORT}`,
    locale: "en-US",
  },
  // viewport after the device preset: the preset carries its own 1280x720 and would win otherwise.
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 },
    },
  ],
  webServer: {
    command: `npm run dev:mock -- --port ${PORT} --strictPort`,
    url: `http://localhost:${PORT}`,
    reuseExistingServer: true,
    timeout: 120_000,
  },
});
