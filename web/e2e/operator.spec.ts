import { expect, type Page, test } from "@playwright/test";

// SaaS operator console against the MSW mocks (src/mocks/operator.ts, privacy.ts): the mock user is an operator and owner
// of "Default". Mock data lives in the page, so every flow navigates inside the app after signing in.

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

const REASON = "Reason (written to the organization's audit log)";

test("operator console: organization list filters and suspension with a reason", async ({ page }) => {
  await signIn(page, "/operator");
  await expect(page.getByRole("heading", { name: "Operator console" })).toBeVisible();
  await expect(page.getByText("3 organizations")).toBeVisible();
  await expect(page.getByRole("link", { name: "Spammy Ltd" })).toBeVisible();

  // State filter applies at once.
  await page.getByLabel("State", { exact: true }).selectOption("trial");
  await expect(page.getByRole("link", { name: "Staging" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Spammy Ltd" })).toHaveCount(0);
  await page.getByLabel("State", { exact: true }).selectOption("");

  // Search is submitted with the form.
  await page.getByLabel("Search organizations").fill("spam");
  await page.getByLabel("Search organizations").press("Enter");
  await expect(page.getByText("1 organizations")).toBeVisible();
  await expect(page.getByRole("link", { name: "Staging" })).toHaveCount(0);

  await page.getByRole("link", { name: "Spammy Ltd" }).click();
  await expect(page).toHaveURL(/\/operator\/orgs\//);
  await expect(page.getByRole("heading", { name: "Spammy Ltd" })).toBeVisible();
  await page.getByRole("button", { name: "Suspend", exact: true }).click();
  const confirm = page.getByRole("button", { name: "Confirm", exact: true });
  await expect(confirm).toBeDisabled();
  await page.getByLabel(REASON).fill("abuse ticket 42");
  await confirm.click();
  await expect(page.getByRole("button", { name: "Unsuspend" })).toBeVisible();
  await expect(page.getByText("Suspended", { exact: true }).first()).toBeVisible();
});

test("operator console: support view after the owner granted access", async ({ page }) => {
  await signIn(page, "/settings/organization");
  await expect(page.getByRole("heading", { name: "openlog support access" })).toBeVisible();
  await page.getByRole("button", { name: "Allow for 24 hours" }).click();
  await expect(page.getByText(/Support access is allowed until/)).toBeVisible();

  await page.getByRole("link", { name: "Operator", exact: true }).first().click();
  await page.getByRole("link", { name: "Default", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Default" })).toBeVisible();
  await page.getByRole("button", { name: "Open support view" }).click();
  await page.getByLabel(REASON).fill("customer ticket 1234");
  await page.getByRole("button", { name: "Confirm", exact: true }).click();

  await expect(page).toHaveURL(/\/hosts/);
  const banner = page.getByTestId("support-banner");
  await expect(banner).toContainText("Support view of Default (read-only)");
  await banner.getByRole("button", { name: "Exit support view" }).click();
  await expect(page).toHaveURL(/\/operator$/);
  await expect(page.getByTestId("support-banner")).toHaveCount(0);
});

test("operator console: schedule and cancel an organization deletion", async ({ page }) => {
  await signIn(page, "/operator/orgs/spammy");
  await expect(page.getByRole("heading", { name: "Organization deletion" })).toBeVisible();
  await expect(page.getByText("No deletion certificates for this tenant.")).toBeVisible();
  await page.getByRole("button", { name: "Schedule deletion" }).click();
  const form = page.getByRole("form", { name: "Schedule deletion" });
  await form.getByLabel(REASON).fill("fraud ticket 7");
  await form.getByLabel("Delete immediately (skip the grace period)").check();
  const submit = form.getByRole("button", { name: "Delete immediately" });
  await expect(submit).toBeDisabled();
  await form.getByLabel("Type the tenant id (spammy) to confirm").fill("spammy");
  await submit.click();

  const pending = page.getByTestId("operator-deletion-pending");
  await expect(pending).toContainText("Scheduled for deletion");
  await expect(pending).toContainText("fraud ticket 7");
  await pending.getByRole("button", { name: "Cancel deletion" }).click();
  await pending.getByRole("button", { name: "Confirm cancellation" }).click();
  await expect(page.getByTestId("operator-deletion-pending")).toHaveCount(0);
  await expect(page.getByText("Cancelled", { exact: true })).toBeVisible();
});
