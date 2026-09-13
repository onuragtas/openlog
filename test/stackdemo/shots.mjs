// Screenshots of the local stack demo (test/stackdemo/run.sh --screenshots). run.sh copies this file
// into a copy of web/ (for @playwright/test) and runs: node stackdemo-shots.mjs <group>...
//
// Groups: before (stack-01 ... stack-14), banner (stack-15), rollout (stack-16), fleet-done (stack-17),
// after-backend (stack-18). Turkish UI, 1440x900. A failed wait is reported, and the screenshot is
// still taken so the problem is visible.
import { chromium } from "@playwright/test";

const base = process.env.STACKDEMO_BASE_URL ?? "http://127.0.0.1:8080";
const email = process.env.STACKDEMO_EMAIL ?? "admin@openlog.local";
const password = process.env.STACKDEMO_PASSWORD ?? "openlog-dev-password";
const out = process.env.STACKDEMO_SHOTS_DIR ?? "shots";
const to = process.env.STACKDEMO_TO ?? "0.9.1";
const WEB = "demo-web-1";
const PLAIN = "demo-plain-1";

const groups = process.argv.slice(2);
const browser = await chromium.launch();
let failures = 0;

async function session(theme = "light", signIn = true) {
  const ctx = await browser.newContext({ baseURL: base, viewport: { width: 1440, height: 900 }, locale: "tr-TR", colorScheme: theme });
  await ctx.addInitScript(([th]) => {
    localStorage.setItem("openlog.theme", th);
    localStorage.setItem("openlog.lang", "tr");
  }, [theme]);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => console.error(`  pageerror: ${e.message}`));
  page.on("console", (m) => m.type() === "error" && console.error(`  console: ${m.text()}`));
  if (signIn) {
    await page.goto("/login");
    await page.locator("input[type=email]").fill(email);
    await page.locator("input[type=password]").fill(password);
    await page.locator("form button[type=submit]").click();
    await page.waitForURL(/\/hosts/);
  }
  return { ctx, page };
}

async function hostId(page, name) {
  const res = await page.request.get("/api/v1/hosts");
  const body = await res.json();
  const h = body.hosts.find((x) => x.host_name === name);
  if (!h) throw new Error(`host ${name} not found`);
  return h.host_id;
}

async function shot(page, name, ready, { fullPage = false } = {}) {
  try {
    await ready?.();
  } catch (e) {
    failures++;
    console.error(`  WARN ${name}: ${e.message.split("\n")[0]}`);
  }
  await page.waitForTimeout(1200); // chart animation, fonts
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  console.log(`saved ${out}/${name}.png`);
}

const T = { timeout: 45_000 };

async function before() {
  const anon = await session("light", false);
  await anon.page.goto("/login");
  await shot(anon.page, "stack-01-login", async () => {
    await anon.page.locator("input[type=email]").fill(email);
    await anon.page.locator("input[type=password]").fill(password);
  });
  await anon.ctx.close();

  const { ctx, page } = await session();
  const web = await hostId(page, WEB);
  await page.goto("/hosts");
  await shot(page, "stack-02-hosts", () => page.getByText(PLAIN).first().waitFor(T));

  await page.goto(`/hosts/${web}?range=30m`);
  await shot(page, "stack-03-host-overview", async () => {
    await page.getByTestId("timeseries-chart").first().waitFor(T);
    await page.waitForFunction(() => document.querySelectorAll("canvas").length >= 4, null, T);
  }, { fullPage: true });

  await page.goto(`/hosts/${web}?tab=services`);
  await shot(page, "stack-04-services", async () => {
    for (const name of [/nginx/i, /redis/i, /postgres/i]) await page.getByTestId("service-card").filter({ hasText: name }).first().waitFor(T);
  }, { fullPage: true });

  await page.goto(`/hosts/${web}?tab=inventory`);
  await shot(page, "stack-05-inventory", () => page.getByTestId("inventory-scroll").waitFor(T));

  await page.goto(`/hosts/${web}?tab=logs&ldisc=nginx&range=30m`);
  await shot(page, "stack-06-host-logs", () => page.getByTestId("log-scroll").getByText(/stackdemo|nginx|GET/).first().waitFor(T));

  await page.goto("/logs?range=30m");
  await shot(page, "stack-07-logs", () => page.getByTestId("log-scroll").waitFor(T));

  await page.goto("/inventory?category=package&q=openssl");
  await shot(page, "stack-08-inventory-search", async () => {
    await page.getByTestId("inventory-scroll").getByText(WEB).first().waitFor(T);
    await page.getByTestId("inventory-scroll").getByText(PLAIN).first().waitFor(T);
  });

  await page.goto("/settings/license-keys");
  await shot(page, "stack-09-settings-license-keys", () => page.getByRole("cell", { name: "bootstrap", exact: true }).waitFor(T));

  await page.goto("/settings/members");
  await shot(page, "stack-10-settings-members", () => page.getByText(email).first().waitFor(T));

  await page.goto("/fleet");
  await shot(page, "stack-11-fleet-overview", () => page.getByTestId("fleet-host-row").nth(1).waitFor(T), { fullPage: true });

  await shot(page, "stack-12-fleet-policy", async () => {
    const h = page.getByRole("heading", { name: "Güncelleme politikası" });
    await h.waitFor(T);
    // The heading is already inside the viewport: scroll it to the top of the scrolling <main>.
    await h.evaluate((el) => el.scrollIntoView({ block: "start" }));
    await page.evaluate(() => document.getElementById("main")?.scrollBy(0, -24));
  });

  await page.goto("/settings/organization");
  await shot(page, "stack-13-version", () => page.getByTestId("server-version").waitFor(T), { fullPage: true });
  await ctx.close();

  const dark = await session("dark");
  const webDark = await hostId(dark.page, WEB);
  await dark.page.goto(`/hosts/${webDark}?range=30m`);
  await shot(dark.page, "stack-14-dark", async () => {
    await dark.page.getByTestId("timeseries-chart").first().waitFor(T);
    await dark.page.waitForFunction(() => document.querySelectorAll("canvas").length >= 4, null, T);
  }, { fullPage: true });
  await dark.ctx.close();
}

async function banner() {
  const { ctx, page } = await session();
  await page.goto("/hosts");
  await shot(page, "stack-15-update-banner", () => page.getByRole("status").filter({ hasText: to }).waitFor(T));
  await ctx.close();
}

async function fleet(name, ready) {
  const { ctx, page } = await session();
  await page.goto("/fleet");
  await shot(page, name, () => ready(page), { fullPage: true });
  await ctx.close();
}

async function afterBackend() {
  const { ctx, page } = await session();
  await page.goto("/settings/organization");
  await shot(page, "stack-18-after-backend-update", async () => {
    await page.getByTestId("server-version").filter({ hasText: to }).waitFor(T);
    await page.getByTestId("updater-status").getByRole("list").waitFor(T);
  }, { fullPage: true });
  await ctx.close();
}

async function apm() {
  const { ctx, page } = await session();
  await page.goto("/apm");
  await shot(page, "stack-19-apm-services", () => page.getByText("catalog", { exact: true }).first().waitFor(T), { fullPage: true });
  await page.goto("/apm/map");
  await shot(page, "stack-20-apm-map", () => page.getByText("orders").first().waitFor(T));
  await ctx.close();
}

async function alert() {
  const rule = process.env.STACKDEMO_ALERT_RULE_ID;
  const { ctx, page } = await session();
  await page.goto("/alerts/incidents");
  await shot(page, "stack-21-alert-incident", () => page.getByText("stackdemo: CPU busy").first().waitFor(T));
  if (rule) {
    await page.goto(`/alerts/rules/${rule}`);
    await shot(page, "stack-22-alert-evaluation-history", () => page.getByTestId("alert-evaluation-history").waitFor(T), { fullPage: true });
  }
  await ctx.close();
}

for (const g of groups) {
  switch (g) {
    case "apm":
      await apm();
      break;
    case "alert":
      await alert();
      break;
    case "before":
      await before();
      break;
    case "banner":
      await banner();
      break;
    case "rollout":
      await fleet("stack-16-fleet-rollout", (page) => page.getByTestId("rollout-details").waitFor(T));
      break;
    case "fleet-done":
      await fleet("stack-17-fleet-done", async (page) => {
        await page.getByTestId("fleet-host-row").nth(1).waitFor(T);
        await page.waitForFunction((v) => [...document.querySelectorAll("[data-testid=fleet-host-row]")].every((r) => r.textContent.includes(v)), to, T);
      });
      break;
    case "after-backend":
      await afterBackend();
      break;
    default:
      console.error(`unknown group ${g}`);
      failures++;
  }
}
await browser.close();
if (failures) console.error(`${failures} screenshot wait(s) failed; see the screenshots`);
