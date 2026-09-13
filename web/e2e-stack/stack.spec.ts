import { expect, test, type Page } from "@playwright/test";

// Real-stack UI checks (playwright.stack.config.ts). Data comes from the e2e agents; see
// docs/operations/e2e.md ("UI phase").

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is required (set by test/e2e/ui_test.go)`);
  return v;
}

async function signIn(page: Page) {
  await page.goto("/");
  await expect(page).toHaveURL(/\/login/);
  await page.getByLabel("Email", { exact: true }).fill(env("E2E_OWNER_EMAIL"));
  await page.getByLabel("Password", { exact: true }).fill(env("E2E_OWNER_PASSWORD"));
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/hosts/);
}

test("owner signs in and inspects the e2e hosts", async ({ page }) => {
  const targetId = env("E2E_TARGET_ID");
  const targetName = env("E2E_TARGET_NAME");
  const plainName = env("E2E_PLAIN_NAME");

  await signIn(page);
  await expect(page.getByRole("heading", { name: "Hosts" })).toBeVisible();
  await expect(page.getByRole("link", { name: `Open host ${plainName}` })).toBeVisible();

  await test.step("host overview charts render", async () => {
    await page.getByRole("link", { name: `Open host ${targetName}` }).click();
    await expect(page).toHaveURL(new RegExp(`/hosts/${targetId}`));
    await expect(page.getByRole("heading", { name: targetName })).toBeVisible();
    await expect(page.getByTestId("timeseries-chart").first()).toBeVisible();
    await expect.poll(() => page.locator("canvas").count()).toBeGreaterThan(0);
  });

  await test.step("services tab lists nginx, redis and postgresql", async () => {
    await page.getByRole("tab", { name: "Services" }).click();
    await expect(page).toHaveURL(/tab=services/);
    const cards = page.getByTestId("service-card");
    for (const name of [/nginx/i, /redis/i, /postgres/i]) {
      await expect(cards.filter({ hasText: name }).first()).toBeVisible();
    }
  });

  await test.step("host logs tab shows nginx lines and filters by file", async () => {
    await page.getByRole("tab", { name: "Logs" }).click();
    await expect(page).toHaveURL(/tab=logs/);
    const rows = page.getByTestId("log-scroll");
    await expect(rows).toContainText(env("E2E_LOG_MARKER"));
    await page.getByLabel("Discovered service").fill("nginx");
    await page.getByLabel("File path").fill("/var/log/nginx/error.log");
    await page.getByRole("button", { name: "Search" }).click();
    await expect(page).toHaveURL(/ldisc=nginx/);
    await expect(page).toHaveURL(/lfile=/);
    await expect(rows).toContainText("open()");
    await expect(rows).not.toContainText('HTTP/1.0" 404');
  });

  await test.step("unknown host shows the not-found state", async () => {
    await page.goto("/hosts/0e2e00000000000000000000000000ff");
    await expect(page.getByText("Host not found")).toBeVisible();
  });

  await test.step("inventory search finds openssl on both hosts", async () => {
    await page.goto("/inventory?category=package&q=openssl");
    const table = page.getByTestId("inventory-scroll");
    await expect(table).toContainText("openssl");
    await expect(table).toContainText(targetName);
    await expect(table).toContainText(plainName);
  });

  await test.step("settings lists the e2e license key", async () => {
    await page.goto("/settings/license-keys");
    await expect(page.getByRole("heading", { name: "Ingest license keys" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "bootstrap", exact: true })).toBeVisible();
    await expect(page.getByText(env("E2E_LICENSE_KEY_PREFIX"), { exact: true })).toBeVisible();
  });
});
