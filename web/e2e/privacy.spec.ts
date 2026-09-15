import { expect, type Page, test } from "@playwright/test";

// Data subject requests against the MSW mocks (src/mocks/privacy.ts): organization export request and download, and the
// account deletion confirmation.

const EMAIL = "admin@openlog.local";
const PASSWORD = "openlog-dev-password";

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill(EMAIL);
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("data export: request an organization export with telemetry and download it", async ({ page }) => {
  await signIn(page, "/settings/organization");
  await expect(page.getByRole("heading", { name: "Data export" })).toBeVisible();
  await expect(page.getByText("No exports yet.")).toBeVisible();

  await page.getByLabel("Logs", { exact: true }).check();
  await expect(page.getByLabel("From", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Request export" }).click();
  await expect(page.getByText("Export queued")).toBeVisible();
  await expect(page.getByText("Ready", { exact: true })).toBeVisible();

  const downloadButton = page.getByRole("button", { name: /Download the export requested/ });
  const [download] = await Promise.all([page.waitForEvent("download"), downloadButton.click()]);
  expect(download.suggestedFilename()).toMatch(/^openlog-export-.+\.zip$/);
});

test("account deletion: the button needs the typed e-mail address and the password", async ({ page }) => {
  await signIn(page, "/settings/profile");
  const del = page.getByRole("button", { name: "Delete my account" });
  await expect(del).toBeDisabled();

  const emailInput = page.getByLabel(`Type your e-mail address (${EMAIL}) to confirm`);
  const passwordInput = page.getByLabel("Your password");
  await emailInput.fill("someone@example.com");
  await passwordInput.fill(PASSWORD);
  await expect(del).toBeDisabled();

  await emailInput.fill(EMAIL);
  await passwordInput.fill("wrong-password");
  await expect(del).toBeEnabled();
  await del.click();
  await expect(page.getByRole("alert").filter({ hasText: "the password is incorrect" })).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/profile/);

  await passwordInput.fill(PASSWORD);
  const [request] = await Promise.all([page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/api/v1/account/delete")), del.click()]);
  expect(request.postDataJSON()).toEqual({ confirm_email: EMAIL, password: PASSWORD });
  await expect(page).toHaveURL(/\/login/);
});
