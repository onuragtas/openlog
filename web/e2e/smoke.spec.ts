import { expect, test } from "@playwright/test";

test("login → hosts → host detail", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/login/);

  // Wrong password is rejected.
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("wrong-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("alert")).toContainText("Incorrect email or password");

  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page).toHaveURL(/\/hosts/);
  await expect(page.getByRole("heading", { name: "Hosts" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Open host web-1" })).toBeVisible();
  await expect(page.getByText("team=payments")).toBeVisible();

  await page.getByRole("link", { name: "Open host web-1" }).click();
  await expect(page).toHaveURL(/\/hosts\/9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b/);
  await expect(page.getByRole("heading", { name: "web-1" })).toBeVisible();

  // Overview charts render (six uPlot canvases).
  await expect(page.getByTestId("timeseries-chart")).toHaveCount(6);
  await expect(page.locator("canvas")).toHaveCount(6);

  // Time range lives in the URL.
  await page.getByRole("button", { name: "6 hours" }).click();
  await expect(page).toHaveURL(/range=6h/);

  // Services tab: discovered services with APM hint.
  await page.getByRole("tab", { name: "Services" }).click();
  await expect(page).toHaveURL(/tab=services/);
  await expect(page.getByTestId("service-card")).toHaveCount(4);
  await expect(page.getByRole("button", { name: "Install openlog-php-agent" })).toBeVisible();
  // One port format everywhere; the process command is shown when the agent sends it.
  await expect(page.getByText("tcp [::]:80", { exact: true })).toBeVisible();
  await expect(page.getByTestId("service-card").filter({ hasText: "Redis" })).toContainText("Command: redis-server");

  // Inventory tab: category filter + search.
  await page.getByRole("tab", { name: "Inventory" }).click();
  await page.getByLabel("Filter items").fill("openssl");
  await expect(page.getByText("dpkg:openssl")).toBeVisible();

  // A session that ends on the server (401) sends the user back to login.
  await page.evaluate(() => sessionStorage.removeItem("openlog.mock.session"));
  await page.getByRole("tab", { name: "Logs" }).click();
  await expect(page).toHaveURL(/\/login/);
  await expect(page.getByRole("alert")).toContainText("Your session has ended");
});

test("logs load older pages; big traces are virtualized", async ({ page }) => {
  await page.goto("/login?redirect=%2Flogs%3Frange%3D24h");
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/logs/);

  // 400 host log records plus container, trace and Kubernetes logs from other mocks in 24h, 200 per page.
  await expect(page.getByText("Newest 200 records")).toBeVisible();
  await page.getByRole("button", { name: "Load older logs" }).click();
  await expect(page.getByText(/Newest (399|400) records/)).toBeVisible();
  // The last full page may need one more request to learn there is nothing older.
  const more = page.getByRole("button", { name: "Load older logs" });
  if (await more.isVisible()) await more.click();
  await expect(page.getByText("No older logs in this range")).toBeAttached();
  await expect(page.getByText(/Newest 4[0-9]{2} records/)).toBeVisible();
  // Only visible rows are in the DOM.
  expect(await page.locator('[data-testid="log-scroll"] [role="row"]').count()).toBeLessThan(120);

  await page.goto("/traces/b16b16b16b16b16b16b16b16b16b16b1");
  await expect(page.getByText("12000 spans")).toBeVisible();
  const rows = page.getByTestId("waterfall-row");
  await expect(rows.first()).toBeVisible();
  expect(await rows.count()).toBeLessThan(100);
  await rows.first().click();
  await page.keyboard.press("End");
  await expect(page).toHaveURL(/span=/);
  await expect(page.locator('[role="treeitem"][aria-selected="true"]')).toHaveAttribute("data-row", "11999");

  // Search steps through matches; collapsing hides subtrees and ArrowRight expands again.
  const tree = page.getByRole("tree", { name: "Span waterfall" });
  await page.getByRole("button", { name: "Collapse all" }).click();
  const collapsedCount = await rows.count();
  expect(collapsedCount).toBeLessThan(100);
  await page.getByRole("searchbox", { name: "Search spans" }).fill("import.chunk 39");
  await page.getByRole("searchbox", { name: "Search spans" }).press("Enter");
  await expect(page.getByTestId("waterfall-match-count")).toHaveText("1 / 1");
  await expect(tree.locator('[aria-selected="true"] mark')).toBeVisible();
  await expect(rows.count()).resolves.toBeLessThan(100);
});
