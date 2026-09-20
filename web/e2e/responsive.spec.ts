import { expect, test, type Page } from "@playwright/test";

// Phone layout (390×844, touch): no screen may scroll horizontally. Content that is meant to
// scroll sideways (wide tables, code, the trace timeline, the service map) does so inside its own
// container, so neither the document nor the <main> scroll area may be wider than the viewport.

const HOST = "9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b";
const CONTAINER = "0a".repeat(32);
const SERVICE = "/apm/services/orders?ns=shop&env=prod";

const ROUTES = [
  "/hosts",
  `/hosts/${HOST}`,
  `/hosts/${HOST}?tab=services`,
  `/hosts/${HOST}?tab=containers`,
  "/containers",
  "/containers?group=true",
  `/containers/${CONTAINER}`,
  `/containers/${CONTAINER}?tab=services`,
  `/containers/${CONTAINER}?tab=logs`,
  `/containers/${CONTAINER}?tab=attributes`,
  `/hosts/${HOST}?tab=inventory`,
  `/hosts/${HOST}?tab=logs`,
  "/apm",
  SERVICE,
  `${SERVICE}&tab=transactions`,
  `${SERVICE}&tab=errors`,
  `${SERVICE}&tab=databases`,
  `${SERVICE}&tab=map`,
  `${SERVICE}&tab=traces`,
  "/apm/map",
  "/logs",
  "/traces/4bf92f3577b34da6a3ce929d0e0e4736",
  "/inventory?category=package",
  "/fleet",
  "/alerts/incidents",
  "/alerts/incidents/i0000000-0000-4000-8000-000000000001",
  "/alerts/rules",
  "/alerts/rules/new",
  "/alerts/rules/r0000000-0000-4000-8000-000000000001",
  "/alerts/channels",
  "/alerts/mutes",
  "/add-data",
  "/add-data/linux",
  "/add-data/apm/php",
  "/settings/organization",
  "/settings/members",
  "/settings/license-keys",
  "/settings/api-keys",
  "/settings/security",
  "/settings/apm-sampling",
  "/settings/usage",
  "/kubernetes",
  "/metrics",
  "/rum",
  "/databases",
  "/slos",
  "/synthetics",
  "/jobs",
  "/profiles",
  "/costs",
  "/traces",
  "/dashboards",
  "/integrations",
];

test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });

async function signIn(page: Page) {
  await page.goto("/login?redirect=%2Fhosts");
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/hosts/);
}

async function expectNoHorizontalOverflow(page: Page, width: string) {
  for (const route of ROUTES) {
    await page.goto(route);
    await expect(page.locator("main h1").first()).toBeVisible();
    await page.waitForLoadState("networkidle");
    // Charts, virtual lists and the map measure their containers after the first paint.
    await page.waitForTimeout(300);
    // (a string expression: the e2e tsconfig has no DOM lib)
    const overflow = await page.evaluate<{ document: number; main: number }>(`(() => {
      const main = document.getElementById("main");
      return {
        document: document.documentElement.scrollWidth - window.innerWidth,
        main: main ? main.scrollWidth - main.clientWidth : 0,
      };
    })()`);
    expect.soft(overflow.document, `${width} ${route}: document.documentElement.scrollWidth <= window.innerWidth`).toBeLessThanOrEqual(0);
    expect.soft(overflow.main, `${width} ${route}: <main> must not scroll horizontally`).toBeLessThanOrEqual(0);
  }
}

test("main screens fit a 390px wide phone without horizontal scrolling", async ({ page }) => {
  test.setTimeout(180_000);
  await signIn(page);
  await expectNoHorizontalOverflow(page, "390px");
});

// 768px is the awkward width: `md` classes are on (stacked tables become tables again, hidden columns come
// back) while the sidebar is still a drawer, so it is the one width where a layout can be wide and
// unconstrained at the same time.
test.describe("tablet", () => {
  test.use({ viewport: { width: 768, height: 1024 }, isMobile: true, hasTouch: true });

  test("main screens fit a 768px wide tablet without horizontal scrolling", async ({ page }) => {
    test.setTimeout(180_000);
    await signIn(page);
    await expectNoHorizontalOverflow(page, "768px");
  });
});

test("navigation drawer and compact top bar", async ({ page }) => {
  await signIn(page);
  const openMenu = page.getByRole("button", { name: "Open navigation menu" });
  const drawer = page.getByRole("dialog", { name: "Main navigation" });

  await openMenu.click();
  await expect(drawer).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(drawer).toHaveCount(0);

  await openMenu.click();
  await drawer.getByRole("link", { name: "Logs" }).click();
  await expect(page).toHaveURL(/\/logs/);
  await expect(drawer).toHaveCount(0);

  // Presets collapse into a select; the custom range opens a bottom sheet.
  await page.getByRole("combobox", { name: "Time range" }).selectOption("6h");
  await expect(page).toHaveURL(/range=6h/);
  await page.getByRole("combobox", { name: "Time range" }).selectOption("custom");
  await expect(page.getByRole("dialog", { name: "Custom" })).toBeVisible();
  await expect(page.getByLabel("From")).toHaveAttribute("type", "datetime-local");
  await page.getByRole("button", { name: "Close" }).click();

  // Language and theme live in the overflow menu.
  await page.getByRole("button", { name: "More options" }).click();
  await expect(page.getByRole("dialog").getByLabel("Language")).toBeVisible();
});

test("span details open in a bottom sheet on phones", async ({ page }) => {
  await signIn(page);
  await page.goto("/traces/4bf92f3577b34da6a3ce929d0e0e4736");
  await page.getByTestId("waterfall-row").first().click();
  const sheet = page.getByRole("dialog", { name: "Span details" });
  await expect(sheet).toBeVisible();
  await expect(page).toHaveURL(/span=/);
  await page.keyboard.press("Escape");
  await expect(sheet).toHaveCount(0);
  await expect(page).not.toHaveURL(/span=/);
});
