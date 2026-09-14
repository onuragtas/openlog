import { expect, test, type Page } from "@playwright/test";

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("APM GA: error inbox workflow, comments and the organization-wide inbox", async ({ page }) => {
  await signIn(page, "/apm/services/frontend?tab=errors&env=prod&ns=shop");
  const inbox = page.getByTestId("error-inbox");
  const groups = inbox.getByRole("button", { name: /^Show error group/ });
  await expect(groups).toHaveCount(2);
  await expect(inbox.getByText("Regressed")).toBeVisible();

  // Bulk resolve: both groups leave the Unresolved tab.
  await inbox.getByLabel("Select all listed error groups").check();
  await page.getByTestId("error-bulk-actions").getByRole("button", { name: "Resolve", exact: true }).click();
  await expect(inbox.getByText("No error groups match these filters in this range.")).toBeVisible();
  await inbox.getByTestId("error-status-tabs").getByRole("button", { name: /^Resolved/ }).click();
  await expect(groups).toHaveCount(2);

  // Group detail: add a comment; it shows in comments and activity.
  await inbox.getByRole("button", { name: "Show error group TypeError" }).click();
  const detail = page.getByTestId("error-group-detail");
  await expect(detail.getByTestId("error-affected")).toContainText("1.4.2");
  await detail.getByLabel("Add a comment").fill("Fixed by the price guard");
  await detail.getByRole("button", { name: "Comment", exact: true }).click();
  await expect(detail.getByTestId("error-comments")).toContainText("Fixed by the price guard");
  await expect(detail.getByTestId("error-activity")).toContainText("commented");

  // Organization-wide inbox (a new page load resets the mock state: every group unresolved again).
  await page.goto("/apm");
  await page.getByRole("link", { name: "Error inbox" }).click();
  await expect(page).toHaveURL(/\/apm\/errors/);
  await expect(page.getByTestId("error-inbox").getByRole("button", { name: /^Show error group/ })).toHaveCount(4);
  await expect(page.getByTestId("error-inbox")).toContainText("catalog");
});

test("APM GA: log → trace → logs of the trace, and span links", async ({ page }) => {
  // "checkout flow" log records carry the mock trace and its span ids (mocks/handlers.ts).
  await signIn(page, "/logs?q=checkout%20flow");
  await page.getByRole("link", { name: /^View trace / }).first().click();
  await expect(page).toHaveURL(/\/traces\/[0-9a-f]{32}/);
  const panel = page.getByTestId("trace-logs");
  await expect(panel.getByRole("heading", { name: "Logs for this trace" })).toBeVisible();
  await panel.getByRole("link", { name: "Open in log explorer" }).click();
  await expect(page).toHaveURL(/\/logs\?.*trace=[0-9a-f]{32}/);

  await page.goto("/logs?q=checkout%20flow");
  await page.getByRole("link", { name: /^View span [0-9a-f]{16} in its trace/ }).first().click();
  await expect(page).toHaveURL(/\/traces\/[0-9a-f]{32}\?span=[0-9a-f]{16}/);
});
