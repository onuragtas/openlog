import { expect, test, type Page } from "@playwright/test";

async function signIn(page: Page, redirect = "/apm") {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("APM: services → service tabs → trace with logs", async ({ page }) => {
  await signIn(page, "/hosts");
  await page.getByRole("navigation", { name: "Main navigation" }).getByRole("link", { name: "APM" }).click();
  await expect(page).toHaveURL(/\/apm$|\/apm\?/);
  await expect(page.getByRole("heading", { name: "APM", exact: true })).toBeVisible();
  const services = page.getByTestId("apm-services");
  await expect(services.getByRole("row")).toHaveCount(4); // header + 3 services
  await expect(services.getByTestId("sparkline")).toHaveCount(3);

  // Filter, then open orders.
  await page.getByLabel("Filter services").fill("ord");
  await expect(services.getByRole("row")).toHaveCount(2);
  await page.getByRole("link", { name: "Open service orders" }).click();
  await expect(page).toHaveURL(/\/apm\/services\/orders\?.*env=prod/);
  await expect(page.getByRole("heading", { name: "orders", level: 1 })).toBeVisible();
  await expect(page.getByTestId("service-hosts").getByRole("link", { name: "Open host web-1" })).toBeVisible();

  // Overview: tiles, four charts, top transactions.
  await expect(page.getByTestId("red-tiles")).toContainText("rpm");
  await expect(page.getByTestId("timeseries-chart")).toHaveCount(4);
  await page.getByRole("button", { name: "Show transaction GET /orders/report" }).click();

  // Transactions: detail with histogram and slowest traces.
  await expect(page).toHaveURL(/tab=transactions/);
  const detail = page.getByTestId("transaction-detail");
  await expect(detail.getByRole("heading", { name: "GET /orders/report" })).toBeVisible();
  await expect(detail.getByTestId("latency-histogram").getByRole("listitem").first()).toBeVisible();
  await expect(detail.getByTestId("slowest-traces").getByRole("listitem")).toHaveCount(5);
  await page.getByLabel("Sort by").selectOption("slowest");
  await expect(page).toHaveURL(/tsort=slowest/);

  // Errors: group detail with the stack trace (application frame emphasized).
  await page.getByRole("tab", { name: "Errors" }).click();
  await page.getByRole("button", { name: "Show error group *errors.errorString" }).click();
  const group = page.getByTestId("error-group-detail");
  await expect(group.getByTestId("stacktrace")).toContainText("main.loadInventory");
  await expect(group.getByTestId("error-samples").getByRole("listitem")).toHaveCount(6);

  // Databases: normalized statements.
  await page.getByRole("tab", { name: "Databases" }).click();
  await expect(page.getByTestId("db-queries")).toContainText("WHERE customer_id = ? AND status IN (?)");

  // Map: React Flow canvas and the accessible connection list.
  await page.getByRole("tab", { name: "Map" }).click();
  await expect(page.getByTestId("service-map").locator(".react-flow__node")).toHaveCount(3);
  await expect(page.getByTestId("map-connections").getByRole("row")).toHaveCount(3);

  // Traces: search → trace waterfall → logs panel.
  await page.getByRole("tab", { name: "Traces" }).click();
  await page.getByLabel("Transaction", { exact: true }).fill("GET /orders/report");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page).toHaveURL(/qtxn=GET/);
  const results = page.getByTestId("trace-results");
  await expect(results.getByRole("row").nth(1)).toContainText("GET /orders/report");
  await page.getByRole("tab", { name: "Traces" }).click();
  await page.getByLabel("Transaction", { exact: true }).fill("");
  await page.getByRole("button", { name: "Search" }).click();
  await page.getByRole("link", { name: /Open trace 4bf92f3577b34da6a3ce929d0e0e4736/ }).first().click();
  await expect(page).toHaveURL(/\/traces\/4bf92f3577b34da6a3ce929d0e0e4736/);
  await expect(page.getByTestId("trace-logs").getByRole("heading", { name: "Logs for this trace" })).toBeVisible();
});

test("APM: full service map, Apdex T edit and services on a host", async ({ page }) => {
  await signIn(page, "/apm/map");
  await expect(page.getByRole("heading", { name: "Service map" })).toBeVisible();
  await expect(page.getByTestId("service-map").locator(".react-flow__node")).toHaveCount(6);
  await page.getByTestId("service-map").locator(".react-flow__node").filter({ hasText: "catalog" }).click();
  await expect(page).toHaveURL(/\/apm\/services\/catalog/);

  await page.getByRole("button", { name: "Edit Apdex T" }).click();
  await page.getByLabel("Apdex T (ms)").fill("250");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("apdex-settings")).toContainText("T = 250 ms");

  await page.goto("/hosts/9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b");
  const section = page.getByTestId("host-apm-services");
  await expect(section.getByRole("heading", { name: "Services on this host" })).toBeVisible();
  await section.getByRole("link", { name: "Open service frontend" }).click();
  await expect(page.getByRole("heading", { name: "frontend", level: 1 })).toBeVisible();
});
