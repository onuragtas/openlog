import { expect, type Page, test } from "@playwright/test";

// Screenshots for reviews: ALERTS_SHOTS_DIR=/path npx playwright test e2e/alerts.spec.ts
const SHOTS = process.env.ALERTS_SHOTS_DIR;

async function shot(page: Page, name: string, fullPage = true) {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage });
}

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("alerts: incident workflow, rule editor with preview, channels and mutes", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, "/alerts");
  await expect(page).toHaveURL(/\/alerts\/incidents/);
  await expect(page.getByRole("heading", { name: "Alerts", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Alerts", exact: true })).toHaveAttribute("data-status", "active");
  await expect(page.getByTestId("incident-row")).toHaveCount(2);
  await shot(page, "alerts-incidents");

  // Incident detail: acknowledge, note, resolve.
  await page.getByRole("link", { name: /system\.cpu\.utilization avg over 5m is 0\.94/ }).click();
  await expect(page.getByTestId("incident-summary")).toContainText("web-1");
  await expect(page.getByTestId("delivery-row")).toHaveCount(3);
  await shot(page, "alerts-incident-detail");
  await page.getByRole("button", { name: "Acknowledge" }).click();
  const timeline = page.getByTestId("incident-timeline");
  await expect(timeline.getByText("acknowledged by admin@openlog.local").first()).toBeVisible();
  await page.getByLabel("Note", { exact: true }).fill("Restarted the batch job.");
  await page.getByRole("button", { name: "Add note" }).click();
  await expect(timeline.getByText("Restarted the batch job.").first()).toBeVisible();
  await page.getByRole("button", { name: "Resolve", exact: true }).click();
  await page.getByLabel("Resolution note (optional)").fill("batch job fixed");
  await page.getByRole("button", { name: "Confirm resolve" }).click();
  await expect(page.getByText(/Resolved by admin@openlog\.local/).first()).toBeVisible();

  // Rules: list, new rule with live preview, save.
  await page.getByRole("link", { name: "Rules", exact: true }).click();
  await expect(page.getByTestId("rule-row")).toHaveCount(4);
  await shot(page, "alerts-rules");
  await page.getByRole("link", { name: "New rule" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Load average high");
  await page.getByLabel("Metric", { exact: true }).fill("system.cpu.load_average.1m");
  await page.getByLabel("Threshold", { exact: true }).fill("1.5");
  await page.getByLabel("Recovery threshold", { exact: true }).fill("1.2");
  await expect(page.getByTestId("preview-summary")).toContainText(/Would have opened \d+ incidents?/, { timeout: 10_000 });
  await expect(page.getByTestId("alert-preview").locator("canvas")).toBeVisible();
  await page.getByLabel(/#ops-alerts/).check();
  await page.getByTestId("alert-preview").scrollIntoViewIfNeeded();
  await shot(page, "alerts-rule-editor");
  await page.getByRole("button", { name: "Create rule" }).click();
  await expect(page.getByRole("heading", { name: "Edit alert rule" })).toBeVisible();
  await page.getByRole("link", { name: "Rules", exact: true }).click();
  await expect(page.getByRole("link", { name: "Load average high" })).toBeVisible();

  // Channels: create a webhook (generated HMAC secret shown once) and send a test.
  await page.getByRole("link", { name: "Channels", exact: true }).click();
  await page.getByRole("button", { name: "New channel" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Receiver webhook");
  await page.getByLabel("Type", { exact: true }).selectOption("webhook");
  await page.getByLabel("Webhook URL").fill("https://receiver.example.com/hook");
  await page.getByRole("button", { name: "Create channel" }).click();
  await expect(page.getByTestId("secret-reveal")).toBeVisible();
  await page.getByRole("button", { name: "Done" }).click();
  const row = page.getByTestId("channel-row").filter({ hasText: "Receiver webhook" });
  await row.getByRole("button", { name: "Send test" }).click();
  await expect(row.getByRole("status")).toContainText("Test notification delivered");
  await shot(page, "alerts-channels");

  // Mutes.
  await page.getByRole("link", { name: "Mutes", exact: true }).click();
  await page.getByRole("button", { name: "New mute" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Deploy window");
  await page.getByRole("button", { name: "Add matcher" }).click();
  await page.getByLabel("Label 1").fill("host.name");
  await page.getByLabel("Value 1").fill("web-1");
  await page.getByRole("button", { name: "Create mute" }).click();
  await expect(page.getByTestId("mute-row").filter({ hasText: "Deploy window" })).toBeVisible();
  await shot(page, "alerts-mutes");

  // Turkish.
  await page.getByRole("link", { name: "Incidents", exact: true }).click();
  await page.getByLabel("Language").selectOption("tr");
  await expect(page.getByRole("heading", { name: "Alarmlar", exact: true })).toBeVisible();
  await page.getByRole("button", { name: /Tümü/ }).click();
  await expect(page.getByTestId("incident-row")).toHaveCount(3);
  await shot(page, "alerts-incidents-tr");
});

test("alerts: rule editor in dark mode", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, "/alerts/rules");
  await page.getByRole("link", { name: "High CPU" }).click();
  await expect(page.getByRole("heading", { name: "Edit alert rule" })).toBeVisible();
  await expect(page.getByTestId("preview-summary")).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId("alert-preview").locator("canvas")).toBeVisible();
  await page.getByTestId("alert-preview").scrollIntoViewIfNeeded();
  await shot(page, "alerts-rule-editor-dark", false);
});

test("alerts: create an alert from a host chart", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, "/hosts");
  await page.getByRole("link", { name: "web-1" }).first().click();
  await page.getByRole("link", { name: "Create alert from this metric: CPU utilization by mode" }).click();
  await expect(page).toHaveURL(/\/alerts\/rules\/new\?/);
  await expect(page.getByLabel("Metric", { exact: true })).toHaveValue("system.cpu.utilization");
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("system.cpu.utilization on web-1");
  await expect(page.getByTestId("filter-row")).toHaveCount(2);
});

test("alerts: viewers cannot change incidents or channels", async ({ page }) => {
  await signIn(page, "/alerts");
  await page.getByLabel("Organization", { exact: true }).selectOption({ label: "Staging" });
  await page.getByRole("link", { name: "Alerts", exact: true }).click();
  await page.getByRole("link", { name: /system\.cpu\.utilization avg over 5m is 0\.94/ }).click();
  await expect(page.getByText("Your role can view incidents but not change them.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Acknowledge" })).toHaveCount(0);
  await page.getByRole("link", { name: "Channels", exact: true }).click();
  await expect(page.getByText("Only admins can create, change or test channels.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Send test" })).toHaveCount(0);
});
