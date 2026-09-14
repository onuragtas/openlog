import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { server } from "@/mocks/server";
import { ApmHintFooter, type ApmHint } from "./ApmHintFooter";

const HOST = "9f3c2a71d4b84e0f8a6b1c2d3e4f5a6b"; // web-1 in mocks/fixtures.ts

function renderHint(hint: ApmHint, serviceName = "PHP-FPM", language = "PHP") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({
    component: () => (
      <div>
        <ApmHintFooter hint={hint} hostId={HOST} serviceName={serviceName} language={language} />
      </div>
    ),
  });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

// Several renders and clicks per test: allow more than the 5 s default on loaded CI machines.
describe("ApmHintFooter", { timeout: 20_000 }, () => {
  it("PHP not installed: real product name, prefilled setup link and fleet install for admins", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    let putBody: unknown = null;
    server.use(
      http.put("*/api/v1/fleet/hosts/:hostId/php-agent", async ({ request, params }) => {
        putBody = { host: params.hostId, ...((await request.json()) as object) };
        return HttpResponse.json({ mode: "auto", updated_at: new Date().toISOString() });
      }),
    );
    const user = userEvent.setup();
    renderHint({ language: "php", agent: "openlog-agent-php", status: "not_installed" });

    const toggle = await screen.findByRole("button", { name: "Install openlog-php-agent" });
    expect(screen.queryByText(/roadmap/i)).not.toBeInTheDocument();
    await user.click(toggle);
    expect(screen.getByText("PHP-FPM runs PHP code. Install openlog-php-agent to get traces and application metrics.")).toBeInTheDocument();
    expect(screen.queryByText("openlog-agent-php")).not.toBeInTheDocument();

    const setup = screen.getByTestId("apm-hint-setup");
    await screen.findByRole("button", { name: "Install via fleet on this host" });
    const href = new URL(setup.getAttribute("href")!, "http://localhost");
    expect(href.pathname).toBe("/add-data/apm/php");
    expect(href.searchParams.get("host")).toBe(HOST);
    expect(href.searchParams.get("hostName")).toBe("web-1");
    expect(href.searchParams.get("service")).toBe("PHP-FPM");

    await user.click(screen.getByRole("button", { name: "Install via fleet on this host" }));
    expect(await screen.findByText(/PHP agent mode set to auto for this host/)).toBeInTheDocument();
    expect(putBody).toEqual({ host: HOST, mode: "auto" });
  });

  it("active: links to the APM service of the host instead of an install prompt", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    server.use(
      http.get("*/api/v1/apm/hosts/:hostId/services", () =>
        HttpResponse.json({ services: [{ service_name: "php-shop", service_namespace: "", environment: "prod", first_seen: "2026-09-01T00:00:00Z", last_seen: "2026-09-14T00:00:00Z" }] }),
      ),
    );
    renderHint({ language: "php", agent: "openlog-agent-php", status: "active" });
    expect(await screen.findByText("APM active")).toBeInTheDocument();
    expect(screen.getByText("PHP-FPM is sending traces with openlog-php-agent.")).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Open in APM" })).toHaveAttribute("href", "/apm/services/php-shop");
    expect(screen.queryByRole("button", { name: /Install/ })).not.toBeInTheDocument();
  });

  it("PHP-FPM pools without socket access: lists them with the restart and manual fixes", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    server.use(
      http.get("*/api/v1/fleet/hosts", () =>
        HttpResponse.json({
          next_cursor: null,
          hosts: [
            {
              host_id: HOST,
              php_access: {
                socket_group: "openlog-php",
                group: "openlog-php",
                group_exists: true,
                agent_member: true,
                grants: "auto",
                pools: [
                  { pool: "www", php_version: "8.2", user: "www-data", unit: "php8.2-fpm.service", access: "ok" },
                  { pool: "example.com", php_version: "7.2", user: "admin", unit: "php7.2-fpm.service", access: "missing" },
                  { pool: "shop", php_version: "7.2", user: "semihyurudu", unit: "php7.2-fpm.service", access: "missing" },
                ],
              },
            },
          ],
        }),
      ),
    );
    renderHint({ language: "php", agent: "openlog-agent-php", status: "not_installed" });
    expect(await screen.findByText("2 PHP-FPM pools cannot send traces")).toBeInTheDocument();
    expect(screen.getByText("example.com · admin · PHP 7.2")).toBeInTheDocument();
    expect(screen.queryByText(/www · www-data/)).not.toBeInTheDocument();
    expect(screen.getByText("sudo systemctl restart openlog-infra-agent")).toBeInTheDocument();
    expect(screen.getByTestId("php-access-manual").textContent).toBe(
      "sudo usermod -aG openlog-php admin && \\\n  sudo usermod -aG openlog-php semihyurudu && \\\n  sudo systemctl reload php7.2-fpm.service",
    );
  });

  it("maps every language hint to its agent and Add data card", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const cases: [ApmHint, string, string][] = [
      [{ language: "nodejs", agent: "openlog-agent-nodejs" }, "Install @openlog/node", "/add-data/apm/node"],
      [{ language: "python", agent: "openlog-agent-python" }, "Install openlog-agent", "/add-data/apm/python"],
      [{ language: "java", agent: "openlog-agent-java" }, "Install openlog-javaagent", "/add-data/apm/java"],
      [{ language: "dotnet", agent: "openlog-agent-dotnet" }, "Install OpenLog.Agent", "/add-data/apm/dotnet"],
      [{ language: "ruby" }, "Install Ruby", "/add-data/otel/sdk"],
    ];
    for (const [hint, button, path] of cases) {
      const view = renderHint(hint, "app", hint.language === "ruby" ? "Ruby" : "X");
      await user.click(await screen.findByRole("button", { name: button }));
      expect(new URL(screen.getByTestId("apm-hint-setup").getAttribute("href")!, "http://localhost").pathname).toBe(path);
      expect(screen.queryByRole("button", { name: "Install via fleet on this host" })).not.toBeInTheDocument();
      view.unmount();
    }
  });

  it("Turkish", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("tr");
    const user = userEvent.setup();
    renderHint({ language: "php", agent: "openlog-agent-php", status: "not_installed" });
    await user.click(await screen.findByRole("button", { name: "openlog-php-agent kur" }));
    expect(screen.getByText("PHP-FPM PHP kodu çalıştırıyor. İz ve uygulama metrikleri için openlog-php-agent kurun.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Kurulum rehberini aç" })).toBeInTheDocument();
    expect(screen.queryByText(/yol haritas/i)).not.toBeInTheDocument();
  });
});
