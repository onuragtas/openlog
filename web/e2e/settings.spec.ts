import { expect, type Page, test } from "@playwright/test";

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("settings: keys are shown once, members, sessions, organization switch", async ({ page }) => {
  await signIn(page, "/settings");
  await expect(page).toHaveURL(/\/settings\/organization/);
  await expect(page.getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
  await expect(page.getByText("default", { exact: true })).toBeVisible();

  // Ingest license key: created, shown once, revoked.
  await page.getByRole("link", { name: "License keys" }).click();
  await expect(page).toHaveURL(/\/settings\/license-keys/);
  await page.getByLabel("Key name").fill("edge nodes");
  await page.getByRole("button", { name: "Create key" }).click();
  const reveal = page.getByTestId("secret-reveal");
  const secret = await reveal.getByRole("textbox").inputValue();
  expect(secret).toMatch(/^olk_[0-9a-f]{48}$/);
  await reveal.getByRole("button", { name: "Done" }).click();
  await expect(reveal).toHaveCount(0);
  const row = page.getByRole("row", { name: /edge nodes/ });
  await expect(row).toContainText(secret.slice(0, 12));
  await expect(page.getByText(secret)).toHaveCount(0);
  await row.getByRole("button", { name: "Revoke" }).click();
  await row.getByRole("button", { name: "Confirm revoke" }).click();
  await expect(row.getByText("Revoked")).toBeVisible();

  // API key.
  await page.getByRole("link", { name: "API keys" }).click();
  await page.getByLabel("Key name").fill("ci");
  await page.getByRole("button", { name: "Create API key" }).click();
  expect(await page.getByTestId("secret-reveal").getByRole("textbox").inputValue()).toMatch(/^ola_/);

  // Invitation link.
  await page.getByRole("link", { name: "Members" }).click();
  await expect(page.getByText("grace@example.com")).toBeVisible();
  await page.getByLabel("Email address").fill("new-hire@example.com");
  await page.getByRole("button", { name: "Create invitation" }).click();
  expect(await page.getByTestId("secret-reveal").getByRole("textbox").inputValue()).toMatch(/\/invite#token=oli_/);
  await expect(page.getByRole("row", { name: /new-hire@example.com/ })).toBeVisible();

  // Sessions.
  await page.getByRole("link", { name: "Security" }).click();
  await expect(page.getByText("This session")).toBeVisible();

  // In an organization where the user is a viewer, key management is hidden.
  await page.getByLabel("Organization", { exact: true }).selectOption({ label: "Staging" });
  await expect(page).toHaveURL(/\/hosts/);
  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page.getByText("Staging · your role: Viewer")).toBeVisible();
  await expect(page.getByRole("link", { name: "License keys" })).toHaveCount(0);
});

test("invitation link signs the new member in", async ({ page }) => {
  await page.goto("/invite#token=oli_mock-invitation");
  await expect(page.getByRole("heading", { name: "Join Default" })).toBeVisible();
  await page.getByLabel("Your name").fill("New Hire");
  await page.getByLabel("Password", { exact: true }).fill("a long enough password");
  await page.getByLabel("Repeat password").fill("a long enough password");
  await page.getByRole("button", { name: "Accept invitation" }).click();
  await expect(page).toHaveURL(/\/hosts/);

  await page.goto("/invite#token=oli_wrong");
  await expect(page.getByRole("alert")).toContainText("invalid");
});
