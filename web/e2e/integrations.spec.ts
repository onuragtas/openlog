import { expect, type Page, test } from "@playwright/test";

// Screenshots for reviews: INTEG_SHOTS_DIR=/path npx playwright test e2e/integrations.spec.ts
const SHOTS = process.env.INTEG_SHOTS_DIR;

async function shot(page: Page, name: string, fullPage = true) {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage });
}

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

const WEB = "9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b";
const DB = "1a2b3c4d5e6f47a8b9c0d1e2f3a4b5c6";

test("integrations: overview, Redis panel with alert preset, needs-configuration help, PostgreSQL panel", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, "/integrations");

  // Overview: every instance across hosts with status counts and a status filter.
  await expect(page.getByRole("heading", { name: "Integrations", exact: true })).toBeVisible();
  const rows = page.getByTestId("integration-row");
  await expect(rows).toHaveCount(6);
  const counts = page.getByTestId("integration-counts");
  await expect(counts.locator('[data-status="enabled"]')).toContainText("4");
  await expect(counts.locator('[data-status="needs_configuration"]')).toContainText("1");
  await counts.locator('[data-status="error"]').click();
  await expect(page).toHaveURL(/status=error/);
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("connection refused");
  await counts.getByRole("button", { name: /^All/ }).click();
  await expect(rows).toHaveCount(6);
  await shot(page, "integ-mock-overview");

  // Redis panel on web-1: curated charts and recommended alerts.
  await page.getByRole("link", { name: "Open the Redis panel on web-1" }).click();
  await expect(page).toHaveURL(new RegExp(`/hosts/${WEB}/integrations/redis/`));
  await expect(page.getByRole("heading", { name: "Redis integration" })).toBeVisible();
  await expect(page.getByTestId("integration-chart")).toHaveCount(6);
  // Recommended alert templates for Redis (alerting.md §2.8): set one up for this instance and create it.
  await expect(page.getByTestId("alert-template")).toHaveCount(2);
  await shot(page, "integ-mock-redis");
  await page.getByRole("button", { name: "Set up Redis memory near maxmemory" }).click();
  const setup = page.getByTestId("template-setup");
  await expect(setup.getByTestId("template-reference")).toContainText("921.6 MiB"); // 90% of maxmemory
  await expect(setup.getByTestId("template-preview")).toBeVisible();
  await setup.getByRole("button", { name: "Create rule" }).click();
  await expect(setup.getByTestId("template-created")).toBeVisible();

  // db-1 services tab → Redis needs configuration: inline form (remote config) instead of charts, the
  // config.yaml snippet as a collapsed manual alternative.
  await page.goto(`/hosts/${DB}?tab=services`);
  const redisCard = page.getByTestId("service-card").filter({ hasText: "Needs configuration" });
  await redisCard.getByTestId("open-integration").click();
  const help = page.getByTestId("integration-config-help");
  await expect(help).toContainText("This integration needs configuration");
  await expect(help).toContainText("NOAUTH");
  await expect(page.getByTestId("integration-chart")).toHaveCount(0);
  await help.getByLabel("Password").fill("s3cret");
  await help.getByRole("button", { name: "Save and send to agent" }).click();
  await expect(page.getByTestId("integration-apply-state")).toContainText("Sent to the agent");
  await expect(help.getByText("A password is saved. Leave empty to keep it.")).toBeVisible();
  const manual = help.getByTestId("integration-manual-config");
  await manual.getByText("Manual configuration (config.yaml)").click();
  await expect(manual.locator("pre")).toContainText("env:OPENLOG_REDIS_PASSWORD");
  await manual.getByRole("button", { name: "Copy snippet" }).click();
  await expect(manual.getByRole("button", { name: "Copied" })).toBeVisible();
  await expect(page.getByRole("switch", { name: "Collect Redis metrics on this host" })).toHaveAttribute("aria-checked", "true");
  await shot(page, "integ-mock-needs-config");

  // PostgreSQL panel: charts filtered by the instance plus the largest tables.
  await page.goto(`/integrations?q=postgres`);
  await page.getByRole("link", { name: "Open the PostgreSQL panel on db-1" }).click();
  await expect(page.getByTestId("integration-chart")).toHaveCount(7);
  await expect(page.getByTestId("integration-top-tables")).toContainText("public.orders");
  // MariaDB partial collection warning.
  await page.goto(`/hosts/${DB}/integrations/mariadb/${encodeURIComponent("/usr/sbin/mariadbd")}`);
  await expect(page.getByTestId("integration-partial")).toContainText("REPLICA MONITOR");
  await expect(page.getByTestId("integration-chart")).toHaveCount(8);

  // Responsive: phone and tablet layouts.
  for (const [w, h, name] of [[390, 844, "390"], [768, 1024, "768"]] as const) {
    await page.setViewportSize({ width: w, height: h });
    await page.goto("/integrations");
    if (w < 768) await expect(page.getByTestId("integration-card")).toHaveCount(6);
    else await expect(rows).toHaveCount(6);
    await shot(page, `integ-mock-overview-${name}`);
    await page.goto(`/hosts/${WEB}/integrations/redis/${encodeURIComponent("/usr/bin/redis-check-rdb")}`);
    await expect(page.getByTestId("integration-chart")).toHaveCount(6);
    await shot(page, `integ-mock-redis-${name}`);
    await page.goto(`/hosts/${DB}/integrations/redis/${encodeURIComponent("/usr/bin/redis-check-rdb")}`);
    await expect(page.getByTestId("integration-config-help")).toBeVisible();
    await shot(page, `integ-mock-needs-config-${name}`);
  }
});
