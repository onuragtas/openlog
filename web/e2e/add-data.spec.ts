import { expect, test, type Page } from "@playwright/test";

const WEB1 = "9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b";

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("Add data: Linux host with a new license key until the host reports", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await signIn(page, "/hosts");
  await expect(page).toHaveURL(/\/hosts/);

  await page.getByTestId("add-data-button").click();
  await expect(page).toHaveURL(/\/add-data$/);
  await expect(page.getByRole("heading", { name: "Add data", level: 1 })).toBeVisible();
  await page.getByRole("searchbox", { name: "Search data sources" }).fill("linux");
  await page.getByTestId("add-data-card").filter({ hasText: "Linux host" }).click();
  await expect(page).toHaveURL(/\/add-data\/linux$/);

  // 1. License key: created inline, shown once.
  await page.getByRole("button", { name: "Create key" }).click();
  await expect(page.getByTestId("key-created")).toBeVisible();
  await page.getByRole("button", { name: "Continue" }).click();

  // 2. Options.
  await page.getByLabel("Host name (optional)").fill("web-42");
  await page.getByLabel("Distribution").selectOption("deb");
  await page.getByRole("button", { name: "Continue" }).click();

  // 3. Commands: key masked until revealed, copied in full.
  const install = page.getByTestId("command-block").first();
  await expect(install).toContainText("install.sh | sudo sh -s --");
  await expect(install).toContainText("--method deb");
  await expect(install).toContainText("--endpoint http://localhost:4318");
  await expect(install).not.toContainText(/olk_[0-9a-f]{48}/);
  await install.getByRole("button", { name: "Copy Install" }).click();
  const copied = await page.evaluate<string>("navigator.clipboard.readText()");
  const key = copied.match(/olk_[0-9a-f]{48}/)?.[0] ?? "";
  expect(copied).toContain(`--license-key ${key}`);
  expect(key).not.toBe("");
  await page.getByRole("button", { name: "Show key" }).click();
  await expect(install).toContainText(key);

  // 4. Verification: waiting until the (mocked) agent reports, then a link to the host.
  await page.getByRole("button", { name: "I ran the commands" }).click();
  const status = page.getByTestId("verify-status");
  await expect(status).toHaveAttribute("data-state", "waiting");
  await expect(status).toContainText("Waiting for host web-42 to report");
  await page.evaluate("window.__openlogMock.reportHost('web-42')");
  await expect(status).toHaveAttribute("data-state", "success", { timeout: 15_000 });
  await expect(status).toContainText("Host web-42 is reporting.");
  await page.getByTestId("verify-open").click();
  await expect(page).toHaveURL(/\/hosts\/[0-9a-f]{32}/);
  await expect(page.getByRole("heading", { name: "web-42" })).toBeVisible();

  // The key never went into the URL.
  expect(page.url()).not.toContain(key);
});

test("host Services APM card opens the PHP setup prefilled with the host and service", async ({ page }) => {
  await signIn(page, `/hosts/${WEB1}?tab=services`);
  await expect(page).toHaveURL(/tab=services/);
  await page.getByRole("button", { name: "Install openlog-php-agent" }).click();
  await expect(page.getByText(/roadmap/i)).toHaveCount(0);
  await page.getByTestId("apm-hint-setup").click();
  await expect(page).toHaveURL(/\/add-data\/apm\/php\?/);
  await expect(page.getByTestId("add-data-host")).toHaveText("web-1");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByLabel("Service name")).toHaveValue("PHP-FPM");
});

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });

  test("install commands scroll inside their blocks, not the page", async ({ page }) => {
    await signIn(page, "/add-data/kubernetes");
    await expect(page).toHaveURL(/\/add-data\/kubernetes/);
    await page.getByRole("radio", { name: /Use a placeholder/ }).check();
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Cluster name").fill("prod-eu-1");
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page.getByTestId("command-block").first()).toBeVisible();
    const overflow = await page.evaluate<{ document: number; main: number }>(`(() => {
      const main = document.getElementById("main");
      return {
        document: document.documentElement.scrollWidth - window.innerWidth,
        main: main ? main.scrollWidth - main.clientWidth : 0,
      };
    })()`);
    expect(overflow.document).toBeLessThanOrEqual(0);
    expect(overflow.main).toBeLessThanOrEqual(0);
  });
});
