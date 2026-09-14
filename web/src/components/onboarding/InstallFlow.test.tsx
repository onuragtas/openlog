import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import type { Onboarding } from "@/api/onboarding";
import type { Host } from "@/api/types";
import { findTarget, type InstallOptions } from "@/lib/install-commands";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { server } from "@/mocks/server";
import { InstallFlow } from "./InstallFlow";

const ONBOARDING: Onboarding = {
  ui_url: { url: "https://openlog.example.com", source: "configured" },
  otlp_http: { url: "https://openlog.example.com:4318", source: "derived_public_url" },
  otlp_grpc: { url: "https://openlog.example.com:4317", source: "derived_public_url" },
  server_version: "0.9.1",
  agent_version: "0.9.1",
  release_channel: "stable",
  cors_enabled: false,
  cors_allowed_origins: [],
  auth_mode: "postgres",
  organization: { id: "org", tenant_id: "default", name: "Default" },
  role: "owner",
  features: { license_keys: true, can_create_license_keys: true, can_list_license_keys: true, fleet_php_install: true, tail_sampling: false },
};

const VIEWER: Onboarding = { ...ONBOARDING, role: "viewer", features: { ...ONBOARDING.features, can_create_license_keys: false, can_list_license_keys: false } };

function renderFlow(targetId: string, onboarding = ONBOARDING, props: { initial?: Partial<InstallOptions>; intervalMs?: number; timeoutMs?: number } = {}) {
  const target = findTarget(targetId)!;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <InstallFlow target={target} onboarding={onboarding} {...props} /> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const host = (id: string, name: string): Host => ({
  host_id: id,
  host_name: name,
  os_description: "Ubuntu 24.04 LTS",
  arch: "amd64",
  agent_version: "0.9.1",
  last_seen: new Date().toISOString(),
  resource_attributes: {},
});

// Multi-step user flows: allow more than the 5 s default on loaded CI machines.
describe("InstallFlow", { timeout: 20_000 }, () => {
  it("creates a key inline, masks it in the commands, reveals and copies it", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderFlow("linux");

    expect(await screen.findByRole("radio", { name: /Create a new key for this install/ })).toBeChecked();
    const next = screen.getByRole("button", { name: "Continue" });
    expect(next).toBeDisabled();
    await user.clear(screen.getByLabelText("Key name"));
    await user.type(screen.getByLabelText("Key name"), "web hosts");
    await user.click(screen.getByRole("button", { name: "Create key" }));
    expect(await screen.findByTestId("key-created")).toHaveTextContent("web hosts");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await user.selectOptions(screen.getByLabelText("Distribution"), "deb");
    await user.click(screen.getByRole("checkbox", { name: /Read Docker container names/ }));
    await user.click(screen.getByRole("button", { name: "Continue" }));

    const install = (await screen.findAllByTestId("command-block"))[0]!;
    expect(install).toHaveTextContent("--method deb");
    expect(install).toHaveTextContent("--no-docker-access");
    expect(install).toHaveTextContent("--endpoint https://openlog.example.com:4318");
    expect(install.textContent).toMatch(/--license-key olk_•+/);
    expect(install.textContent).not.toMatch(/olk_[0-9a-f]{48}/);
    // Derived endpoints ask for confirmation.
    expect(screen.getByText(/Derived from OPENLOG_PUBLIC_URL/)).toBeInTheDocument();

    await user.click(within(install).getByRole("button", { name: "Copy Install" }));
    const copied = await navigator.clipboard.readText();
    const key = copied.match(/olk_[0-9a-f]{48}/)?.[0];
    expect(key).toBeDefined();
    expect(copied).toContain(`--license-key ${key}`);

    await user.click(screen.getByRole("button", { name: "Show key" }));
    expect(install).toHaveTextContent(key!);
    await user.click(screen.getByRole("button", { name: "Hide key" }));
    expect(install).not.toHaveTextContent(key!);
  });

  it("keeps a pasted key in the browser and warns about a prefix mismatch", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const seen: string[] = [];
    const onRequest = ({ request }: { request: Request }) => {
      seen.push(`${request.method} ${request.url}`);
    };
    server.events.on("request:start", onRequest);
    const user = userEvent.setup();
    renderFlow("apm/node");

    await user.click(await screen.findByRole("radio", { name: /I have a key/ }));
    await user.selectOptions(await screen.findByLabelText("Which key?"), "lk-1");
    const input = screen.getByLabelText("License key value");
    expect(input).toHaveAttribute("type", "password");
    await user.type(input, "olk_ffff0000pastedvalue");
    expect(screen.getByRole("alert")).toHaveTextContent("olk_9f3c2a71");
    await user.clear(input);
    await user.type(input, "olk_9f3c2a71pastedvalue");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.type(screen.getByLabelText("Service name"), "checkout");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await user.click(await screen.findByRole("button", { name: "Show key" }));
    const run = screen.getAllByTestId("command-block").find((b) => b.getAttribute("data-block") === "run")!;
    expect(run).toHaveTextContent("export OPENLOG_LICENSE_KEY=olk_9f3c2a71pastedvalue");
    expect(run).toHaveTextContent("node --require @openlog/node/register server.js");
    // No registry check result: the package comes from the GitHub release, and a note says so.
    const install = screen.getAllByTestId("command-block").find((b) => b.getAttribute("data-block") === "install")!;
    expect(install).toHaveTextContent("npm install https://github.com/onuragtas/openlog/releases/download/v0.9.1/openlog-node-0.9.1.tgz");
    expect(screen.getByTestId("install-notes")).toHaveTextContent("attached to the GitHub release");
    server.events.removeListener("request:start", onRequest);
    expect(seen.some((s) => s.includes("pastedvalue"))).toBe(false);
    expect(seen.some((s) => s.startsWith("POST"))).toBe(false);
  });

  it("members without create permission get the placeholder", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID);
    const user = userEvent.setup();
    renderFlow("docker", VIEWER);

    expect(await screen.findByRole("radio", { name: /Create a new key/ })).toBeDisabled();
    expect(screen.getByText("Only admins can create license keys.")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Use a placeholder/ })).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    const run = (await screen.findAllByTestId("command-block"))[0]!;
    expect(run).toHaveTextContent("-e OPENLOG_LICENSE_KEY='<LICENSE_KEY>'");
    expect(screen.getByTestId("install-notes")).toHaveTextContent("Replace <LICENSE_KEY>");
    expect(screen.queryByRole("button", { name: "Show key" })).not.toBeInTheDocument();
  });

  it("waits for a new host, shows troubleshooting after the timeout and links the host once it reports", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const existing = [host("a".repeat(32), "db-1")];
    let report = false;
    server.use(
      http.get("*/api/v1/hosts", () => HttpResponse.json({ hosts: report ? [...existing, host("b".repeat(32), "web-42")] : existing })),
    );
    const user = userEvent.setup();
    renderFlow("linux", ONBOARDING, { intervalMs: 50, timeoutMs: 1 });

    await user.click(await screen.findByRole("radio", { name: /Use a placeholder/ }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.type(screen.getByLabelText("Host name (optional)"), "web-42");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("button", { name: "I ran the commands" }));

    const status = await screen.findByTestId("verify-status");
    expect(status).toHaveAttribute("data-state", "waiting");
    expect(status).toHaveTextContent("Waiting for host web-42 to report");
    expect(await screen.findByTestId("verify-timeout", {}, { timeout: 3000 })).toBeInTheDocument();
    expect(screen.getByTestId("verify-tips")).toHaveTextContent("journalctl -u openlog-infra-agent");
    expect(screen.getByTestId("verify-tips")).toHaveTextContent("https://openlog.example.com:4318");

    report = true;
    const link = await screen.findByTestId("verify-open", {}, { timeout: 3000 });
    expect(screen.getByTestId("verify-status")).toHaveAttribute("data-state", "success");
    expect(screen.getByTestId("verify-status")).toHaveTextContent("Host web-42 is reporting.");
    expect(link).toHaveAttribute("href", `/hosts/${"b".repeat(32)}`);
    expect(screen.queryByTestId("verify-timeout")).not.toBeInTheDocument();
  });

  it("waits for the APM service by name", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    let services: { service_name: string }[] = [];
    server.use(
      http.get("*/api/v1/apm/services", () =>
        HttpResponse.json({
          step: "60s",
          services: services.map((s) => ({ ...s, service_namespace: "", environment: "", language: "", version: "" })),
        }),
      ),
    );
    const user = userEvent.setup();
    renderFlow("apm/python", ONBOARDING, { intervalMs: 50, initial: { serviceName: "billing" } });
    await user.click(await screen.findByRole("radio", { name: /Use a placeholder/ }));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(screen.getByLabelText("Service name")).toHaveValue("billing");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("button", { name: "I ran the commands" }));
    expect(await screen.findByTestId("verify-status")).toHaveTextContent("Waiting for service billing");
    services = [{ service_name: "other" }, { service_name: "billing" }];
    const link = await screen.findByTestId("verify-open", {}, { timeout: 3000 });
    expect(link).toHaveAttribute("href", "/apm/services/billing");
  });

  it("integration cards need no key and link to the integrations page", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderFlow("integrations/mysql");
    expect(await screen.findByTestId("key-not-needed")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(screen.getByText("Nothing to choose for this data source.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    const blocks = await screen.findAllByTestId("command-block");
    expect(blocks.map((b) => b.getAttribute("data-block"))).toEqual(["sqlUser", "passwordFile", "agentConfig", "restart"]);
    await user.click(screen.getByRole("button", { name: "I ran the commands" }));
    expect(await screen.findByRole("link", { name: "Open integrations" })).toBeInTheDocument();
  });
});
