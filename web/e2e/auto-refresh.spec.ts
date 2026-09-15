import { expect, test, type Page } from "@playwright/test";

// Global auto-refresh control next to the time range picker, against the MSW mocks (npm run dev:mock).

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("auto-refresh interval is kept in the URL across pages and remembered", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, "/logs?range=15m");
  await expect(page).toHaveURL(/\/logs/);

  await page.getByRole("button", { name: "Auto-refresh: off" }).and(page.locator(":visible")).click();
  await page.getByRole("menuitemradio", { name: "30s" }).click();
  await expect(page).toHaveURL(/refresh=30s/);
  await expect(page.getByRole("button", { name: "Auto-refresh every 30s" }).and(page.locator(":visible"))).toBeVisible();

  await page.getByRole("link", { name: "Metrics", exact: true }).click();
  await expect(page).toHaveURL(/\/metrics.*refresh=30s/);

  // Remembered per browser: a link without the param uses the stored choice.
  await page.goto("/traces?range=1h");
  await expect(page.getByRole("button", { name: "Auto-refresh every 30s" }).and(page.locator(":visible"))).toBeVisible();
});

test("absolute ranges disable auto-refresh; phones get one icon menu", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, "/logs?from=1700000000000&to=1700003600000");
  await expect(page).toHaveURL(/\/logs/);
  await page.getByRole("button", { name: "Auto-refresh" }).and(page.locator(":visible")).click();
  await expect(page.getByRole("menuitem", { name: "Refresh now" })).toBeVisible();
  await expect(page.getByRole("menuitemradio", { name: "30s" })).toHaveAttribute("aria-disabled", "true");
  await page.keyboard.press("Escape");

  const hScroll = await page.evaluate(() => {
    const w = globalThis as unknown as { innerWidth: number; document: { documentElement: { scrollWidth: number } } };
    return w.document.documentElement.scrollWidth > w.innerWidth;
  });
  expect(hScroll).toBe(false);
});
