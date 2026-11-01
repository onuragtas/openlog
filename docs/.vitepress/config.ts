// The documentation site (https://onuragtas.github.io/openlog/), built from the Markdown in this repository.
//
// **There is one copy of everything.** A page under integrations/ only includes the README next to the agent it
// describes, so the guide people read on the site and the one they read on GitHub cannot drift apart. What that costs is
// links: a README's relative links are written for where it lives, not for the page that includes it, so they are
// resolved here against the file's real location. Anything that does not become a page goes to GitHub, so the dead link
// check of the build stays on and a broken link fails it.
import { defineConfig } from "vitepress";

const REPO = "https://github.com/onuragtas/openlog";

/** The page under integrations/ for each README included there. */
const INTEGRATIONS: Record<string, string> = {
  "agents/browser": "browser",
  "agents/swift": "ios",
  "agents/kotlin": "android",
  "agents/dart": "flutter",
  "agents/go": "go",
  "agents/node": "node",
  "agents/python": "python",
  "agents/java": "java",
  "agents/dotnet": "dotnet",
  "agents/php/ext": "php",
  "agents/infra": "infra",
  "agents/ebpf": "ebpf",
};

/** The file a page's links are relative to: the README it includes, or the page itself. */
function sourceOf(relativePath: string): string {
  const slug = relativePath.match(/^integrations\/(.+)\.md$/)?.[1];
  const dir = Object.entries(INTEGRATIONS).find(([, s]) => s === slug)?.[0];
  return dir ? `${dir}/README.md` : `docs/${relativePath}`;
}

/** Where a relative link in the file at `source` should go on the site. */
export function resolveLink(href: string, source: string): string {
  if (/^([a-z][a-z0-9+.-]*:|#|\/)/i.test(href)) return href;
  const url = new URL(href, `file:///${source}`);
  const path = url.pathname.slice(1);
  const hash = url.hash;
  const excluded = /^docs\/operations\/(benchmarks\/|live-validation-)/.test(path);
  if (!excluded && (/^docs\/(contracts|operations|guide|tr\/guide)\/.+\.md$/.test(path) || path === "docs/features.md")) return `/${path.slice("docs/".length)}${hash}`;
  const integration = path.endsWith("/README.md") ? INTEGRATIONS[path.slice(0, -"/README.md".length)] : undefined;
  if (integration) return `/integrations/${integration}${hash}`;
  return `${REPO}/blob/master/${path}${hash}`;
}

export default defineConfig({
  title: "openlog",
  description: "Open-source observability: infrastructure, APM, logs, browser and mobile monitoring, and alerts.",
  base: "/openlog/",
  cleanUrls: true,
  lastUpdated: true,
  // The plan is the project's own working material, in Turkish. It stays in the repository.
  srcExclude: ["plan/**", "operations/benchmarks/**", "operations/live-validation-*.md", "node_modules/**"],
  // Runbooks point at a local UI, and those links are meant for whoever is following them.
  ignoreDeadLinks: "localhostLinks",
  // English is the default and lives at the root. Turkish covers the pages people read first: the integration guides are
  // included from the READMEs next to each agent and the reference is written for implementers, so both stay in English
  // rather than become a second copy to keep up to date. The Turkish menu says which pages those are.
  locales: {
    root: { label: "English", lang: "en" },
    tr: {
      label: "Türkçe",
      lang: "tr",
      link: "/tr/",
      description: "Açık kaynak gözlemlenebilirlik: altyapı, APM, loglar, tarayıcı ve mobil izleme ve alarmlar.",
      themeConfig: {
        nav: [
          { text: "Başlangıç", link: "/tr/guide/quickstart" },
          { text: "Entegrasyonlar", link: "/tr/guide/send-data" },
          { text: "Ürün turu (EN)", link: "/features" },
          { text: "Referans (EN)", link: "/contracts/api" },
        ],
        sidebar: [
          {
            text: "Başlangıç",
            items: [
              { text: "Hızlı başlangıç", link: "/tr/guide/quickstart" },
              { text: "Entegrasyon seç", link: "/tr/guide/send-data" },
              { text: "Ürün turu (EN)", link: "/features" },
            ],
          },
          {
            text: "Web ve mobil uygulamalar",
            items: [
              { text: "Ürün analitiği", link: "/tr/guide/product-analytics" },
              { text: "Crash raporlama", link: "/tr/guide/crash-reporting" },
              { text: "Tarayıcı (EN)", link: "/integrations/browser" },
              { text: "iOS, Swift (EN)", link: "/integrations/ios" },
              { text: "Android, Kotlin (EN)", link: "/integrations/android" },
              { text: "Flutter, Dart (EN)", link: "/integrations/flutter" },
            ],
          },
          {
            text: "Backend APM (EN)",
            items: [
              { text: "Go", link: "/integrations/go" },
              { text: "Node.js", link: "/integrations/node" },
              { text: "Python", link: "/integrations/python" },
              { text: "Java", link: "/integrations/java" },
              { text: ".NET", link: "/integrations/dotnet" },
              { text: "PHP", link: "/integrations/php" },
            ],
          },
          {
            text: "Sunucular ve kümeler (EN)",
            items: [
              { text: "Altyapı ajanı", link: "/integrations/infra" },
              { text: "Tüm makine profilleyici", link: "/integrations/ebpf" },
              { text: "Kubernetes", link: "/operations/kubernetes" },
            ],
          },
          {
            text: "Referans (EN)",
            collapsed: true,
            items: [
              { text: "HTTP API", link: "/contracts/api" },
              { text: "Yapılandırma", link: "/contracts/config" },
              { text: "OQL", link: "/contracts/oql" },
              { text: "Alarmlar", link: "/contracts/alerting" },
              { text: "Güncelleme", link: "/operations/upgrading" },
            ],
          },
        ],
        editLink: { pattern: `${REPO}/edit/master/docs/:path`, text: "Bu sayfayı GitHub'da düzenle" },
        outline: { label: "Bu sayfada" },
        docFooter: { prev: "Önceki", next: "Sonraki" },
        lastUpdated: { text: "Son güncelleme" },
        darkModeSwitchLabel: "Görünüm",
        lightModeSwitchTitle: "Açık temaya geç",
        darkModeSwitchTitle: "Koyu temaya geç",
        sidebarMenuLabel: "Menü",
        returnToTopLabel: "Başa dön",
        langMenuLabel: "Dil değiştir",
        notFound: {
          title: "SAYFA BULUNAMADI",
          quote: "Aradığın sayfa taşınmış veya hiç var olmamış olabilir.",
          linkLabel: "ana sayfaya dön",
          linkText: "Ana sayfaya dön",
        },
      },
    },
  },
  markdown: {
    // These documents write placeholders such as <uuid> and <server> in running text. They are text, not elements.
    html: false,
    config(md) {
      md.core.ruler.push("openlog_links", (state) => {
        const source = sourceOf((state.env as { relativePath?: string }).relativePath ?? "");
        for (const block of state.tokens) {
          for (const token of block.children ?? []) {
            if (token.type !== "link_open") continue;
            const href = token.attrGet("href");
            if (href) token.attrSet("href", resolveLink(href, source));
          }
        }
      });
      // A template engine reads the page, and "{{ value }}" in a sentence about templates would be run as code.
      for (const rule of ["text", "code_inline"] as const) {
        const render = md.renderer.rules[rule] ?? ((tokens, i) => md.utils.escapeHtml(tokens[i]!.content));
        md.renderer.rules[rule] = (tokens, i, options, env, self) => render(tokens, i, options, env, self).replace(/\{\{/g, "&#123;&#123;");
      }
    },
  },
  themeConfig: {
    nav: [
      { text: "Get started", link: "/guide/quickstart" },
      { text: "Integrations", link: "/guide/send-data" },
      { text: "Product tour", link: "/features" },
      { text: "Reference", link: "/contracts/api" },
    ],
    socialLinks: [{ icon: "github", link: REPO }],
    search: {
      provider: "local",
      options: {
        locales: {
          tr: {
            translations: {
              button: { buttonText: "Ara", buttonAriaLabel: "Ara" },
              modal: {
                displayDetails: "Ayrıntıları göster",
                resetButtonTitle: "Aramayı temizle",
                backButtonTitle: "Aramayı kapat",
                noResultsText: "Sonuç bulunamadı:",
                footer: { selectText: "seç", navigateText: "gezin", closeText: "kapat" },
              },
            },
          },
        },
      },
    },
    editLink: { pattern: `${REPO}/edit/master/docs/:path`, text: "Edit this page on GitHub" },
    sidebar: [
      {
        text: "Get started",
        items: [
          { text: "Quickstart", link: "/guide/quickstart" },
          { text: "Choose an integration", link: "/guide/send-data" },
          { text: "Product tour", link: "/features" },
        ],
      },
      {
        text: "Web and mobile apps",
        items: [
          { text: "Browser", link: "/integrations/browser" },
          { text: "iOS (Swift)", link: "/integrations/ios" },
          { text: "Android (Kotlin)", link: "/integrations/android" },
          { text: "Flutter (Dart)", link: "/integrations/flutter" },
          { text: "Product analytics", link: "/guide/product-analytics" },
          { text: "Crash reporting", link: "/guide/crash-reporting" },
        ],
      },
      {
        text: "Backend (APM)",
        items: [
          { text: "Go", link: "/integrations/go" },
          { text: "Node.js", link: "/integrations/node" },
          { text: "Python", link: "/integrations/python" },
          { text: "Java", link: "/integrations/java" },
          { text: ".NET", link: "/integrations/dotnet" },
          { text: "PHP", link: "/integrations/php" },
        ],
      },
      {
        text: "Hosts and clusters",
        items: [
          { text: "Infra agent", link: "/integrations/infra" },
          { text: "Whole-host profiler", link: "/integrations/ebpf" },
          { text: "Kubernetes", link: "/operations/kubernetes" },
        ],
      },
      {
        text: "Run openlog",
        collapsed: true,
        items: [
          { text: "Upgrading", link: "/operations/upgrading" },
          { text: "Scaling", link: "/operations/scaling" },
          { text: "Disk space", link: "/operations/disk-space" },
          { text: "Tiered storage", link: "/operations/tiered-storage" },
          { text: "Tail sampling", link: "/operations/tail-sampling" },
          { text: "SSO and SCIM", link: "/operations/sso" },
          { text: "Reports", link: "/operations/reports" },
          { text: "Terraform", link: "/operations/terraform" },
          { text: "MCP server", link: "/operations/mcp" },
          { text: "Hosted (SaaS) mode", link: "/operations/saas" },
        ],
      },
      {
        text: "Reference",
        collapsed: true,
        items: [
          { text: "HTTP API", link: "/contracts/api" },
          { text: "Configuration", link: "/contracts/config" },
          { text: "OQL", link: "/contracts/oql" },
          { text: "Alerting", link: "/contracts/alerting" },
          { text: "SLOs", link: "/contracts/slo" },
          { text: "Semantic conventions", link: "/contracts/semantic-conventions" },
          { text: "APM", link: "/contracts/apm" },
          { text: "Real user monitoring", link: "/contracts/rum" },
          { text: "Mobile protocol", link: "/contracts/mobile-agent" },
          { text: "Product analytics", link: "/contracts/analytics" },
          { text: "Crash reporting", link: "/contracts/mobile-crash" },
          { text: "Profiles", link: "/contracts/profiles" },
          { text: "Database monitoring", link: "/contracts/db-monitoring" },
        ],
      },
    ],
  },
});
