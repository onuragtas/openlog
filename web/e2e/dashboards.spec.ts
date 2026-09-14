import { expect, test, type Page } from "@playwright/test";

// Smoke test of the OQL query console and custom dashboards against the MSW mocks (npm run dev:mock).

const INFRA = "d0000000-0000-4000-8000-000000000001";

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("query console runs a query; dashboards list and grid on desktop", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, "/query");
  await expect(page).toHaveURL(/\/query/);
  await expect(page.getByRole("heading", { name: "Query", exact: true })).toBeVisible();

  // CodeMirror (lazy chunk) replaces the textarea fallback; both are a textbox named "OQL query".
  await expect(page.getByTestId("oql-codemirror")).toBeVisible();
  const editor = page.getByRole("textbox", { name: "OQL query" });
  await editor.click();
  await page.keyboard.type("SELECT count(*) FROM Log FACET severity");
  await page.keyboard.press("Escape");
  await page.keyboard.press("ControlOrMeta+Enter");
  await expect(page.getByTestId("bar-list")).toBeVisible();
  await expect(page).toHaveURL(/q=SELECT/);
  await page.getByRole("button", { name: "Table", exact: true }).click();
  await expect(page.getByTestId("result-table")).toBeVisible();
  await expect(page.getByTestId("query-history")).toContainText("FACET severity");

  // Invalid query: diagnostics with line and column.
  await editor.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type("SELECT count(*) FROM Lgo");
  await expect(page.getByRole("list", { name: "Query problems" })).toContainText('Line 1, column 22: unknown event type "Lgo"');

  await page.getByRole("link", { name: "Dashboards", exact: true }).click();
  await expect(page.getByTestId("dashboard-row")).toHaveCount(2);
  await page.getByRole("link", { name: "Infrastructure overview" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Infrastructure overview" })).toBeVisible();
  await expect(page.getByTestId("dashboard-grid-layout")).toBeVisible();
  await expect(page.getByTestId("dashboard-widget")).toHaveCount(8);
  await expect(page.getByTestId("timeseries-chart").first()).toBeVisible();
  await expect(page.getByTestId("billboard-value").first()).toHaveText(/\d/);

  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await expect(page.getByRole("button", { name: "Actions for Log volume" })).toBeVisible();
});

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });

  test("seeded dashboard stacks into one column without horizontal scrolling", async ({ page }) => {
    await signIn(page, `/dashboards/${INFRA}`);
    await expect(page.getByRole("heading", { level: 1, name: "Infrastructure overview" })).toBeVisible();
    await expect(page.getByTestId("dashboard-grid-stacked")).toBeVisible();
    await expect(page.getByTestId("dashboard-widget")).toHaveCount(8);
    await expect(page.getByRole("group", { name: "Dashboard variables" })).toBeVisible();
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(300);
    for (const path of [`/dashboards/${INFRA}`, "/dashboards", "/query?q=SELECT%20count(*)%20FROM%20Log%20FACET%20severity%20TIMESERIES"]) {
      await page.goto(path);
      await expect(page.locator("main h1").first()).toBeVisible();
      await page.waitForLoadState("networkidle");
      await page.waitForTimeout(300);
      const overflow = await page.evaluate<{ document: number; main: number }>(`(() => {
        const main = document.getElementById("main");
        return { document: document.documentElement.scrollWidth - window.innerWidth, main: main ? main.scrollWidth - main.clientWidth : 0 };
      })()`);
      expect.soft(overflow.document, `${path}: document must not scroll horizontally`).toBeLessThanOrEqual(0);
      expect.soft(overflow.main, `${path}: <main> must not scroll horizontally`).toBeLessThanOrEqual(0);
    }
  });
});
