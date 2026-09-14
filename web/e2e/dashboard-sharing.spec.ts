import { expect, test, type Page } from "@playwright/test";

// Dashboard cross-widget filters, version history, share links (public read-only view) and the service map
// hosts/containers toggle against the MSW mocks (npm run dev:mock).

const INFRA = "d0000000-0000-4000-8000-000000000001";

async function signIn(page: Page, redirect: string) {
  await page.goto(`/login?redirect=${encodeURIComponent(redirect)}`);
  await page.getByLabel("Email", { exact: true }).fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
}

/** Client-side navigation: the MSW mock state lives in the page, so a full reload would forget the created link. */
async function navigateInApp(page: Page, path: string) {
  await page.evaluate(`(() => {
    window.history.pushState({}, "", ${JSON.stringify(path)});
    window.dispatchEvent(new PopStateEvent("popstate"));
  })()`);
}

async function horizontalOverflow(page: Page) {
  return page.evaluate<{ document: number; main: number }>(`(() => {
    const main = document.getElementById("main");
    return { document: document.documentElement.scrollWidth - window.innerWidth, main: main ? main.scrollWidth - main.clientWidth : 0 };
  })()`);
}

test("dashboard filters, version history and a share link opened read-only on a phone", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, `/dashboards/${INFRA}`);
  await expect(page.getByRole("heading", { level: 1, name: "Infrastructure overview" })).toBeVisible();

  // A clicked facet value filters the dashboard (URL `filters`) and can be cleared.
  const facet = page.getByTestId("result-table").getByTestId("facet-filter").first();
  await expect(facet).toBeVisible();
  const value = (await facet.textContent())!;
  await facet.click();
  await expect(page.getByTestId("filter-chip")).toHaveText(`transaction.name = ${value}`);
  await expect(page).toHaveURL(/filters=/);
  await page.getByTestId("filter-clear").click();
  await expect(page.getByTestId("filter-bar")).toHaveCount(0);

  // Version history lists the stored versions with the current one first.
  await page.getByTestId("open-history").click();
  const history = page.getByRole("dialog", { name: "Version history" });
  await expect(history.getByTestId("version-item")).toHaveCount(2);
  await expect(history.getByTestId("version-item").first()).toContainText("Current");
  await page.keyboard.press("Escape");
  await expect(history).toHaveCount(0);

  // Share links: enable for the organization, create a link, copy it once.
  await page.getByTestId("open-share").click();
  const share = page.getByRole("dialog", { name: "Share links" });
  await share.getByRole("button", { name: "Enable share links" }).click();
  await share.getByLabel("Label").fill("Wall screen");
  await share.getByRole("button", { name: "Create link" }).click();
  const url = await share.getByTestId("share-url").inputValue();
  expect(url).toMatch(/\/shared\/dashboards\/olds_[A-Za-z0-9_-]{43}$/);
  await expect(share.getByTestId("share-item")).toContainText("Wall screen");
  await page.keyboard.press("Escape");

  // The public view: no app navigation, read-only widgets, no horizontal scrolling at 390px.
  await page.setViewportSize({ width: 390, height: 844 });
  await navigateInApp(page, new URL(url).pathname);
  await expect(page.getByTestId("shared-dashboard")).toBeVisible();
  await expect(page.getByRole("heading", { level: 1, name: "Infrastructure overview" })).toBeVisible();
  await expect(page.getByText("Shared dashboard (read-only)")).toBeVisible();
  await expect(page.getByRole("button", { name: "Open navigation menu" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Edit" })).toHaveCount(0);
  await expect(page.getByTestId("billboard-value").first()).toHaveText(/\d/);
  await page.waitForLoadState("networkidle");
  await page.waitForTimeout(300);
  const overflow = await horizontalOverflow(page);
  expect.soft(overflow.document, "shared dashboard: document must not scroll horizontally").toBeLessThanOrEqual(0);
  expect.soft(overflow.main, "shared dashboard: <main> must not scroll horizontally").toBeLessThanOrEqual(0);

  // An unknown link explains itself.
  await navigateInApp(page, "/shared/dashboards/olds_unknown");
  await expect(page.getByRole("heading", { name: "Link not available" })).toBeVisible();
});

test("dashboard history, share and report panels fit a 390px phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page, `/dashboards/${INFRA}`);
  await expect(page.getByRole("heading", { level: 1, name: "Infrastructure overview" })).toBeVisible();
  for (const [button, dialog] of [
    ["open-history", "Version history"],
    ["open-share", "Share links"],
    ["open-reports", "Scheduled reports"],
  ] as const) {
    await page.getByTestId(button).click();
    const sheet = page.getByRole("dialog", { name: dialog });
    await expect(sheet).toBeVisible();
    // The sheet slides in: wait until it has settled inside the viewport.
    await expect.poll(async () => {
      const box = await sheet.boundingBox();
      return box ? Math.round(box.x) >= 0 && box.x + box.width <= 391 : false;
    }, { message: `${dialog} fits the viewport` }).toBe(true);
    const overflow = await horizontalOverflow(page);
    expect.soft(overflow.document, `${dialog}: no horizontal scrolling`).toBeLessThanOrEqual(0);
    await page.keyboard.press("Escape");
    await expect(sheet).toHaveCount(0);
  }
  await page.getByTestId("open-reports").click();
  const reports = page.getByRole("dialog", { name: "Scheduled reports" });
  await expect(reports.getByTestId("report-item")).toContainText("Morning infrastructure summary");
});

test("service map: host and container counts on service nodes can be hidden", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await signIn(page, "/apm/map");
  const map = page.getByTestId("service-map");
  await expect(map.getByTestId("map-node-counts").first()).toBeVisible();
  const toggle = page.getByTestId("map-infra-toggle");
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(map.getByTestId("map-node-counts")).toHaveCount(0);
  await toggle.click();
  await expect(map.getByTestId("map-node-counts").first()).toBeVisible();
});
