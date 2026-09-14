import { expect, type Page, test } from "@playwright/test";

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
}

test("single sign-on settings: domain, OIDC wizard, test sign-in, enforcement and SCIM", async ({ page }) => {
  await signIn(page, "/settings/sso");
  await expect(page).toHaveURL(/\/settings\/sso/);
  await expect(page.getByRole("link", { name: "Single sign-on" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Verified domains" })).toBeVisible();

  // Claim and verify a domain.
  await page.getByRole("textbox", { name: "Domain", exact: true }).fill("acme.example.com");
  await page.getByRole("button", { name: "Add domain" }).click();
  const row = page.getByTestId("sso-domain-acme.example.com");
  await expect(row.getByRole("textbox", { name: "Record value" })).toHaveValue(/^openlog-domain-verification=/);
  await row.getByRole("button", { name: "Check DNS" }).click();
  await expect(row.getByText("Verified", { exact: true })).toBeVisible();

  // Connection wizard (OIDC).
  await page.getByRole("button", { name: "2. Service provider" }).click();
  await expect(page.getByRole("textbox", { name: "Redirect URI" })).toHaveValue(/\/api\/v1\/sso\/oidc\/callback$/);
  await page.getByRole("button", { name: "Next" }).click();
  await page.getByRole("textbox", { name: "Issuer URL" }).fill("https://idp.example.com");
  await page.getByRole("textbox", { name: "Client ID" }).fill("openlog");
  await page.getByLabel("Client secret", { exact: true }).fill("s3cret");
  await page.getByRole("button", { name: "Save connection" }).click();
  await expect(page.getByText("Connection saved.")).toBeVisible();

  // Test sign-in: a full navigation to the identity provider and back.
  await page.getByRole("button", { name: "Test sign-in" }).click();
  await expect(page).toHaveURL(/\/settings\/sso\?sso_test=ok/);
  await expect(page.getByText("Test sign-in succeeded.")).toBeVisible();
  await expect(page.getByTestId("sso-last-test")).toContainText("admin@openlog.local");

  // Enforcement with a break-glass owner.
  await page.getByRole("checkbox", { name: /admin@openlog\.local/ }).check();
  await page.getByRole("button", { name: "Enforce SSO" }).click();
  await page.getByRole("button", { name: "Enforce now" }).click();
  await expect(page.getByRole("button", { name: "Stop enforcing" })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Single sign-on is enforced." })).toBeVisible();

  // SCIM token, shown once.
  await page.getByRole("textbox", { name: "Token name" }).fill("okta");
  await page.getByRole("button", { name: "Create SCIM token" }).click();
  expect(await page.getByTestId("secret-reveal").getByRole("textbox").inputValue()).toMatch(/^ols_[0-9a-f]{48}$/);

  // Phone width: the page does not scroll horizontally.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("heading", { name: "SCIM provisioning" })).toBeVisible();
  // e2e files have no DOM lib: evaluate the expression as a string.
  expect(Number(await page.evaluate("document.documentElement.scrollWidth - window.innerWidth"))).toBeLessThanOrEqual(1);
});

test("sign in with single sign-on from the login page", async ({ page }) => {
  await page.addInitScript(() => sessionStorage.setItem("openlog.mock.sso.preset", "enabled-oidc"));
  await page.goto("/login?redirect=%2Fapm");
  await page.getByRole("button", { name: "Continue with SSO" }).click();
  await expect(page.getByRole("heading", { name: "Sign in with single sign-on" })).toBeVisible();

  await page.getByLabel("Work e-mail").fill("someone@elsewhere.example");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("not set up for this e-mail domain");

  await page.getByLabel("Work e-mail").fill("admin@openlog.local");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page).toHaveURL(/\/apm/);
});

test("the login page explains failed SSO sign-ins", async ({ page }) => {
  await page.goto("/login?sso_error=domain_not_verified");
  await expect(page.getByRole("alert")).toContainText("not verified for this organization");
  await page.goto("/login?sso_error=not-a-code");
  await expect(page.getByRole("alert")).toHaveCount(0);
});
