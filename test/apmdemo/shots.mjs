// Screenshots of the APM demo (test/apmdemo/run.sh shots). run.sh copies this file into a copy of web/
// (for @playwright/test) and runs it. 1440x900, English UI (dark map + Turkish list variants included).
import { chromium } from "@playwright/test";

const base = process.env.APMDEMO_BASE_URL ?? "http://127.0.0.1:31080";
const email = process.env.APMDEMO_EMAIL ?? "admin@apmdemo.local";
const password = process.env.APMDEMO_PASSWORD ?? "apmdemo-password";
const hostId = process.env.APMDEMO_HOST_ID ?? "a0de0000000000000000000000000001";
const out = process.env.APMDEMO_SHOTS_DIR ?? "shots";
const range = process.env.APMDEMO_RANGE ?? "15m";

const browser = await chromium.launch();
let failures = 0;
const T = { timeout: 45_000 };

async function session({ theme = "light", lang = "en" } = {}) {
  const ctx = await browser.newContext({ baseURL: base, viewport: { width: 1440, height: 900 }, locale: lang === "tr" ? "tr-TR" : "en-US", colorScheme: theme });
  await ctx.addInitScript(([th, lg]) => {
    localStorage.setItem("openlog.theme", th);
    localStorage.setItem("openlog.lang", lg);
  }, [theme, lang]);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => console.error(`  pageerror: ${e.message}`));
  await page.goto("/login");
  await page.locator("input[type=email]").fill(email);
  await page.locator("input[type=password]").fill(password);
  await page.locator("form button[type=submit]").click();
  await page.waitForURL(/\/hosts/);
  return { ctx, page };
}

async function shot(page, name, ready, { fullPage = false } = {}) {
  try {
    await ready?.();
  } catch (e) {
    failures++;
    console.error(`  WARN ${name}: ${e.message.split("\n")[0]}`);
  }
  await page.waitForTimeout(1500);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  console.log(`saved ${out}/${name}.png`);
}

const svc = (name, extra = "") => `/apm/services/${encodeURIComponent(name)}?range=${range}&ns=shop&env=demo${extra}`;

{
  const { ctx, page } = await session();
  await page.goto(`/apm?range=${range}`);
  await shot(page, "apm-01-services", () => page.getByTestId("apm-services").getByRole("row").nth(3).waitFor(T));

  await page.goto(svc("frontend"));
  await shot(page, "apm-02-service-overview", () => page.getByTestId("red-tiles").waitFor(T), { fullPage: true });

  await page.goto(svc("orders", "&tab=transactions&tsort=slowest&txn=" + encodeURIComponent("GET /orders/report")));
  await shot(page, "apm-03-slow-transaction", () => page.getByTestId("latency-histogram").waitFor(T), { fullPage: true });

  await page.goto(svc("orders", "&tab=errors"));
  await page.getByTestId("error-groups").waitFor(T).catch(() => undefined);
  await page.getByRole("button", { name: /Show error group/ }).first().click().catch(() => undefined);
  await shot(page, "apm-04-errors", () => page.getByTestId("stacktrace").waitFor(T), { fullPage: true });

  await page.goto(svc("frontend", "&tab=errors"));
  await shot(page, "apm-05-errors-frontend", () => page.getByTestId("error-groups").waitFor(T));

  await page.goto(svc("orders", "&tab=databases"));
  await shot(page, "apm-06-databases", () => page.getByTestId("db-queries").waitFor(T));

  await page.goto(`/apm/map?range=${range}`);
  await shot(page, "apm-07-service-map", () => page.locator(".react-flow__node").nth(4).waitFor(T), { fullPage: true });

  await page.goto(svc("orders", "&tab=traces&qerr=true"));
  await page.getByTestId("trace-results").waitFor(T).catch(() => undefined);
  await shot(page, "apm-08-trace-search", () => page.getByTestId("trace-results").waitFor(T));
  await page.getByRole("link", { name: /Open trace/ }).first().click().catch(() => undefined);
  await shot(page, "apm-09-trace-logs", () => page.getByTestId("trace-logs").getByRole("listitem").first().waitFor(T), { fullPage: true });

  await page.goto(`/hosts/${hostId}?range=${range}`);
  await shot(page, "apm-10-host-services", () => page.getByTestId("host-apm-services").waitFor(T));

  await page.goto(svc("catalog", "&tab=map"));
  await shot(page, "apm-11-service-map-tab", () => page.locator(".react-flow__node").nth(1).waitFor(T));
  await ctx.close();
}
{
  const { ctx, page } = await session({ theme: "dark", lang: "tr" });
  await page.goto(`/apm/map?range=${range}`);
  await shot(page, "apm-12-service-map-dark-tr", () => page.locator(".react-flow__node").nth(4).waitFor(T));
  await page.goto(`/apm?range=${range}`);
  await shot(page, "apm-13-services-dark-tr", () => page.getByTestId("apm-services").waitFor(T));
  await ctx.close();
}

await browser.close();
if (failures) {
  console.error(`${failures} screenshot(s) did not reach their ready state`);
  process.exitCode = 1;
}
