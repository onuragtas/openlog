import { expect, test, type Page } from "@playwright/test";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

const OUT = path.resolve(process.cwd(), "../docs/images");

// Ids the MSW mocks actually serve (src/mocks): guessed ids render empty or 404 screens.
const HOST_WEB = "9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b";
const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";
const PROFILE_SERVICE = "checkout-api";
const DB_INSTANCE = "db1.internal:5432";
const RUM_APP = "shop-web";
/** The trace's errored "POST /pricing" span (mocks/fixtures.ts): selecting it fills the span details panel. */
const ERROR_SPAN = "f6f6f6f6f6f6f6f6";

const taken = new Map<string, string>();

test.describe.configure({ mode: "serial" });
test.beforeAll(() => fs.mkdirSync(OUT, { recursive: true }));

async function login(page: Page) {
  await page.goto("/");
  // Wait for the form, not for the URL: goto() returns before the redirect to /login, so a url check ran
  // too early and every screenshot came out as the login page (three byte-identical files).
  const email = page.getByLabel("Email", { exact: true });
  await email.waitFor({ state: "visible", timeout: 30_000 });
  await email.fill("admin@openlog.local");
  await page.getByLabel("Password", { exact: true }).fill("openlog-dev-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/hosts/, { timeout: 30_000 });
  await expect(page.getByRole("heading", { name: "Hosts" })).toBeVisible({ timeout: 30_000 });
}

/** Screenshots the current page once it has settled. */
async function capture(page: Page, name: string) {
  await page.waitForLoadState("networkidle").catch(() => {});
  // Charts draw on canvas after the data arrives; give them a beat rather than racing them.
  await page.waitForTimeout(900);
  // A screenshot of the login page is worse than none: it looks like a result and is not one.
  if (/\/login/.test(page.url())) throw new Error(`${name}: fell back to the login page`);
  const file = path.join(OUT, `${name}.png`);
  await page.screenshot({ path: file, fullPage: false });
  taken.set(name, crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex"));
}

async function shot(page: Page, url: string, name: string) {
  await page.goto(url);
  await capture(page, name);
}

/** Opens a list, clicks the first row that leads to a detail page, then screenshots it.
 *
 * Located by href prefix rather than by accessible name: the names are copy, differ per screen and change
 * with the locale, and guessing one regex per list cost a 2.7-minute run each time. The route shape is
 * already known from router.tsx. */
async function shotFirst(page: Page, listUrl: string, hrefPrefix: string, name: string) {
  await page.goto(listUrl);
  await page.waitForLoadState("networkidle").catch(() => {});
  const link = page.locator(`a[href^="${hrefPrefix}"]`).first();
  await link.waitFor({ state: "visible", timeout: 20_000 });
  const before = page.url();
  await link.click();
  await mustNavigate(page, before, name);
  await capture(page, name);
}

/** Fails when a click did not leave the list.
 *
 * Two captures (slo-detail, synthetic-detail) were screenshots of their list page: the click only
 * highlighted the row. They passed every check — the files existed and no two were byte-identical,
 * because a highlighted row differs from an unhighlighted one. Only looking at them caught it. */
async function mustNavigate(page: Page, before: string, name: string) {
  await page.waitForURL((u) => u.toString() !== before, { timeout: 15_000 }).catch(() => {});
  if (page.url() === before) throw new Error(`${name}: the click did not open a detail page (still ${before})`);
}

/** Opens the first row of a list whose rows are not links.
 *
 * SLOs, synthetics, jobs and vulnerabilities render clickable rows rather than anchors (probed: their
 * `main` carries no a[href] at all), so neither the accessible name nor an href prefix finds them. */
async function shotFirstRow(page: Page, listUrl: string, listTestId: string, name: string) {
  await page.goto(listUrl);
  await page.waitForLoadState("networkidle").catch(() => {});
  const row = page.getByTestId(listTestId).locator("tbody tr").first();
  await row.waitFor({ state: "visible", timeout: 20_000 });
  // The row itself is not always the target: SLOs and synthetics put the name in a button inside the
  // row, and clicking the row only selected it. Prefer that button, fall back to the row.
  const button = row.getByRole("button").first();
  const target = (await button.count()) > 0 ? button : row;
  const before = page.url();
  await target.click();
  await mustNavigate(page, before, name);
  await capture(page, name);
}

/** Screenshots each tab of a screen by URL, which is steadier than clicking. */
async function shotTabs(page: Page, base: string, tabs: readonly string[], prefix: string) {
  const join = base.includes("?") ? "&" : "?";
  for (const tab of tabs) await shot(page, `${base}${join}tab=${tab}`, `${prefix}-${tab}`);
}

test("menu", async ({ page }) => {
  test.setTimeout(300_000);
  await login(page);

  // ---- the main menu, in sidebar order ----
  await shot(page, "/add-data", "add-data");
  await shot(page, "/hosts", "hosts");
  await shot(page, "/containers", "containers");
  await shot(page, "/costs", "costs");
  await shot(page, "/kubernetes", "kubernetes");
  await shot(page, "/kubernetes/workloads", "kubernetes-workloads");
  await shot(page, "/kubernetes/pods", "kubernetes-pods");
  await shot(page, "/kubernetes/nodes", "kubernetes-nodes");
  await shot(page, "/integrations", "integrations");
  await shot(page, "/integrations/cloud", "integrations-cloud");
  await shot(page, "/apm", "apm");
  await shot(page, "/apm/map", "apm-map");
  await shot(page, "/apm/errors", "apm-errors");
  await shot(page, "/apm/agents", "apm-agents");
  await shot(page, "/rum", "rum");
  await shot(page, "/profiles", "profiles");
  await shot(page, "/databases", "databases");
  await shot(page, "/slos", "slos");
  await shot(page, "/synthetics", "synthetics");
  await shot(page, "/jobs", "jobs");
  await shot(page, "/vulnerabilities", "vulnerabilities");
  await shot(page, "/logs", "logs");
  await shot(page, "/traces", "traces");
  // The explorer keeps the selected metric in `mq`; without it the page is the "choose a metric" state.
  await shot(page, `/metrics?mq=${encodeURIComponent(JSON.stringify([{ i: "A", m: "http.server.request.duration", a: "p95" }]))}`, "metrics");
  // The console runs the query in `q` as it mounts (QueryConsole), so a bare /query is only the empty builder.
  await shot(page, `/query?q=${encodeURIComponent("SELECT count(*) FROM Log FACET severity TIMESERIES")}`, "query");
  await shot(page, "/dashboards", "dashboards");
  // Inventory search shows only its form until a search runs, and the search lives in the URL
  // (routes/inventory-search.tsx reads category/q from it).
  await shot(page, "/inventory?category=package&q=openssl", "inventory");
  await shot(page, "/fleet", "fleet");

});

test("alerts", async ({ page }) => {
  test.setTimeout(300_000);
  await login(page);
  // /alerts redirects to /alerts/incidents (router: alertsIndexRoute beforeLoad), so it is one screen.
  await shot(page, "/alerts/incidents", "alerts-incidents");
  await shot(page, "/alerts/rules", "alerts-rules");
  await shot(page, "/alerts/rules/new", "alerts-rules-new");
  await shot(page, "/alerts/templates", "alerts-templates");
  await shot(page, "/alerts/channels", "alerts-channels");
  await shot(page, "/alerts/routing", "alerts-routing");
  await shot(page, "/alerts/mutes", "alerts-mutes");

});

test("settings", async ({ page }) => {
  test.setTimeout(300_000);
  await login(page);
  // /settings redirects to /settings/organization (router: settingsIndexRoute), so it is not a screen of its own.
  for (const s of [
    "organization", "profile", "members", "license-keys", "api-keys", "browser-keys",
    "source-maps", "security", "audit-log", "apm-sampling", "usage", "sso", "status-page",
  ]) {
    await shot(page, `/settings/${s}`, `settings-${s}`);
  }

});

test("detail tabs", async ({ page }) => {
  test.setTimeout(300_000);
  await login(page);
  await shotTabs(page, `/hosts/${HOST_WEB}`, ["overview", "services", "containers", "inventory", "vulnerabilities", "logs"], "host");
  await shotTabs(page, "/apm/services/orders", ["overview", "transactions", "errors", "databases", "map", "traces"], "apm-service");
  await shotTabs(page, `/rum/${RUM_APP}`, ["overview", "pages", "sessions"], "rum-app");
  await shotTabs(page, `/profiles?service=${PROFILE_SERVICE}&type=cpu`, ["flame", "functions"], "profiles");
  await shotTabs(page, `/databases/instance?instance=${encodeURIComponent(DB_INSTANCE)}`, ["activity", "queries", "sessions"], "database");
  // ?span selects a span: without it the "Span details" panel stays in its empty state (routes/trace.tsx).
  await shot(page, `/traces/${TRACE}?span=${ERROR_SPAN}`, "trace-detail");

});

test("detail rows", async ({ page }) => {
  test.setTimeout(300_000);
  await login(page);
  // Detail pages whose ids the mocks do not export: reached by opening the first row.
  await shotFirst(page, "/containers", "/containers/", "container-detail");
  await shotFirst(page, "/kubernetes/pods", "/kubernetes/pods/", "kubernetes-pod");
  await shotFirstRow(page, "/slos", "slo-list", "slo-detail");
  await shotFirstRow(page, "/synthetics", "synthetics-list", "synthetic-detail");
  await shotFirstRow(page, "/jobs", "jobs-list", "job-detail");
  await shotFirstRow(page, "/vulnerabilities", "vulnerabilities-list", "vulnerability-detail");
  // Ids observed in the mock DOM: these two lists are real links, so the detail page is reachable directly.
  await shot(page, "/dashboards/d0000000-0000-4000-8000-000000000001", "dashboard");
  await shot(page, "/alerts/incidents/i0000000-0000-4000-8000-000000000001", "alerts-incident");
  // No rum-session capture: the mock serves no session rows for shop-web (probed: the sessions tab
  // carries no session links), so the old session-timeline image has no equivalent here.
});

test("no two screens look the same", async () => {
  // Hash the directory rather than trusting the in-memory map: the first attempt wrote identical files
  // and a "file exists" check passed anyway.
  const byHash = new Map<string, string[]>();
  for (const f of fs.readdirSync(OUT).filter((f) => f.endsWith(".png"))) {
    const h = crypto.createHash("sha256").update(fs.readFileSync(path.join(OUT, f))).digest("hex");
    byHash.set(h, [...(byHash.get(h) ?? []), f]);
  }
  const collisions = [...byHash.values()].filter((names) => names.length > 1);
  expect(collisions, `identical screenshots: ${JSON.stringify(collisions)}`).toEqual([]);
  console.log(`captured ${fs.readdirSync(OUT).filter((f) => f.endsWith(".png")).length} screenshots`);
});
