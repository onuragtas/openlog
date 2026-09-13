import { expect, type Page, test } from "@playwright/test";

// Screenshots for reviews: FLEET_SHOTS_DIR=/path npx playwright test e2e/fleet.spec.ts
const SHOTS = process.env.FLEET_SHOTS_DIR;

async function shot(page: Page, name: string, fullPage = true) {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage });
}

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("fleet: policy change starts a rollout that progresses; pause, resume, rollback, host exceptions", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, "/fleet");
  await expect(page).toHaveURL(/\/fleet/);
  await expect(page.getByRole("heading", { name: "Fleet", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Fleet" })).toHaveAttribute("data-status", "active");
  await expect(page.getByText(/Update available: \d+ agents are behind 0\.4\.0\./)).toBeVisible();
  await expect(page.getByTestId("fleet-host-row").first()).toBeVisible();
  await shot(page, "fleet-overview");

  // Policy: notify → automatic, waves 10/50/100.
  await page.getByRole("radio", { name: /Automatic/ }).check();
  await page.getByLabel("Waves (%)").fill("10, 50, 100");
  await page.getByLabel("Halt at failure rate (%)").fill("5");
  await page.getByRole("button", { name: "Save policy" }).click();
  await expect(page.getByText("Policy saved")).toBeVisible();

  // Rollout appears and advances through its waves (the mock advances on every refresh).
  const details = page.getByTestId("rollout-details");
  await expect(details.getByRole("heading", { name: "Upgrade to 0.4.0" })).toBeVisible();
  await expect(page.getByTestId("rollout-wave")).toContainText(/Wave [23] of 3/, { timeout: 20_000 });
  await expect(page.getByTestId("rollout-failed")).toHaveText("1", { timeout: 20_000 });
  await details.scrollIntoViewIfNeeded();
  await shot(page, "fleet-rollout");

  // Pause and resume with confirmation.
  await page.getByRole("button", { name: "Pause" }).click();
  await page.getByRole("button", { name: "Confirm pause" }).click();
  await expect(details.getByText("Paused", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Resume" }).click();
  await page.getByRole("button", { name: "Confirm resume" }).click();
  await expect(details.getByText("Active", { exact: true })).toBeVisible();

  // Rollback to the previous version.
  await page.getByLabel("Version to roll back to").selectOption("0.3.0");
  await page.getByRole("button", { name: "Roll back" }).click();
  await page.getByRole("button", { name: "Confirm rollback" }).click();
  await expect(details.getByRole("heading", { name: "Rollback to 0.3.0" })).toBeVisible();

  // Host exceptions: hold one host, pin another.
  await page.getByRole("searchbox", { name: "Search agents" }).fill("web-0");
  await expect(page).toHaveURL(/q=web-0/);
  const row = page.getByTestId("fleet-host-row").filter({ hasText: "web-02" });
  await row.getByRole("button", { name: "Hold" }).click();
  await expect(row.getByText("Held")).toBeVisible();
  const other = page.getByTestId("fleet-host-row").filter({ hasText: "web-03" });
  await other.getByRole("button", { name: "Pin" }).click();
  await other.getByLabel("Version to pin web-03 to").fill("0.3.0");
  await other.getByRole("button", { name: "Pin", exact: true }).click();
  await expect(other.getByText("Pinned to 0.3.0")).toBeVisible();
  await page.locator("#fleet-hosts-title").scrollIntoViewIfNeeded();
  await shot(page, "fleet-hosts", false);

  await page.getByRole("heading", { name: "Update policy" }).scrollIntoViewIfNeeded();
  await shot(page, "fleet-policy", false);

  // Dark mode and Turkish.
  await page.evaluate("document.documentElement.classList.add('dark')");
  await page.getByRole("heading", { name: "Fleet", exact: true }).scrollIntoViewIfNeeded();
  await shot(page, "fleet-dark");
  await page.getByLabel("Language").selectOption("tr");
  await expect(page.getByRole("heading", { name: "Filo", exact: true })).toBeVisible();
  await shot(page, "fleet-tr", false);
});

test("fleet: viewers cannot change the policy", async ({ page }) => {
  await signIn(page, "/fleet");
  await page.getByLabel("Organization", { exact: true }).selectOption({ label: "Staging" });
  await page.getByRole("link", { name: "Fleet" }).click();
  await expect(page.getByText("Only admins can change the update policy and rollouts.")).toBeVisible();
  await expect(page.getByLabel("Waves (%)")).toBeDisabled();
  await expect(page.getByRole("button", { name: "Save policy" })).toHaveCount(0);
});
