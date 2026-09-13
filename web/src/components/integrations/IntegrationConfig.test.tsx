import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { mockStoredPassword, resetMockIntegrationSettings } from "@/mocks/integrationSettings";
import type { IntegrationName } from "@/api/integrationSettings";
import { ApplyNotice, HostIntegrationToggle, IntegrationConfigPanel, useApplyState } from "./IntegrationConfig";

const HOST = "host-db-1";
const INSTANCE = "/usr/bin/redis-server";
const HINT = "# Redis requires AUTH.\nintegrations:\n  redis:\n    password: env:OPENLOG_REDIS_PASSWORD";

function Harness({ integration = "redis", canManage = true }: { integration?: IntegrationName; canManage?: boolean }) {
  const apply = useApplyState(HOST, null);
  return (
    <>
      <ApplyNotice phase={apply.phase} />
      <IntegrationConfigPanel
        hostId={HOST}
        hostName="db-1"
        instance={INSTANCE}
        integration={integration}
        status="needs_configuration"
        error="authentication required (NOAUTH)"
        hint={HINT}
        canManage={canManage}
        onSaved={apply.markSaved}
      />
      <HostIntegrationToggle hostId={HOST} hostName="db-1" integration={integration} name="Redis" canManage={canManage} onSaved={apply.markSaved} />
    </>
  );
}

function renderHarness(props: { integration?: IntegrationName; canManage?: boolean } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, ...render(<QueryClientProvider client={client}><Harness {...props} /></QueryClientProvider>) };
}

describe("integration configuration from the UI", () => {
  beforeEach(async () => {
    resetMockIntegrationSettings();
    await login(MOCK_EMAIL, MOCK_PASSWORD);
  });

  it("saves credentials, never shows the password again and follows the agent applying them", async () => {
    const user = userEvent.setup();
    const { client } = renderHarness();

    const endpoint = await screen.findByLabelText("Endpoint");
    await user.type(endpoint, "localhost");
    await user.tab();
    expect(screen.getByText("Enter host:port or unix:/absolute/path.")).toBeInTheDocument();
    expect(endpoint).toHaveAttribute("aria-invalid", "true");
    await user.clear(endpoint);
    await user.type(endpoint, "127.0.0.1:6379");

    await user.type(screen.getByLabelText("Username"), "openlog");
    await user.type(screen.getByLabelText("Password"), "s3cret");
    await user.click(screen.getByRole("button", { name: "Save and send to agent" }));

    expect(await screen.findByText("Sent to the agent, waiting for it to apply the settings…")).toBeInTheDocument();
    // The form shows the stored values; the password field is empty and only says a password is saved.
    expect(await screen.findByText("A password is saved. Leave empty to keep it.")).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByLabelText("Username")).toHaveValue("openlog");
    expect(mockStoredPassword("is-1")).toBe("s3cret");
    expect(document.body.textContent).not.toContain("s3cret");

    // The mock agent applies the revision on a later poll.
    await act(() => client.invalidateQueries({ queryKey: ["integration-settings", HOST] }));
    expect(await screen.findByText("The agent applied the settings, waiting for its next status report…")).toBeInTheDocument();

    // Saving again without typing a password keeps it.
    await user.click(screen.getByRole("button", { name: "Save and send to agent" }));
    await waitFor(() => expect(screen.getByText("Sent to the agent, waiting for it to apply the settings…")).toBeInTheDocument());
    expect(mockStoredPassword("is-1")).toBe("s3cret");

    // Manual configuration stays available, collapsed.
    const manual = screen.getByTestId("integration-manual-config");
    expect(manual).not.toHaveAttribute("open");
    await user.click(within(manual).getByText("Manual configuration (config.yaml)"));
    expect(manual).toHaveAttribute("open");
    expect(within(manual).getByLabelText("Configuration snippet")).toHaveTextContent("env:OPENLOG_REDIS_PASSWORD");
  });

  it("disables the integration on the host and turns it back on", async () => {
    const user = userEvent.setup();
    renderHarness();
    const toggle = await screen.findByRole("switch", { name: "Collect Redis metrics on this host" });
    await waitFor(() => expect(toggle).toBeEnabled());
    expect(toggle).toHaveAttribute("aria-checked", "true");

    await user.click(toggle);
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
    await waitFor(() => expect(toggle).toBeEnabled());
    await user.click(toggle);
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  });

  it("nginx asks only for the stub_status URL", async () => {
    const user = userEvent.setup();
    renderHarness({ integration: "nginx" });
    const endpoint = await screen.findByLabelText("Endpoint");
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    expect(screen.getByText("URL of the stub_status page. Leave empty to let the agent find it.")).toBeInTheDocument();
    await user.type(endpoint, "127.0.0.1:80/nginx_status");
    await user.click(screen.getByRole("button", { name: "Save and send to agent" }));
    expect(screen.getByText("Enter an http:// or https:// URL.")).toBeInTheDocument();
  });

  it("is read-only below admin", async () => {
    renderHarness({ canManage: false });
    expect(await screen.findByLabelText("Endpoint")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save and send to agent" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Collect Redis metrics on this host" })).toBeDisabled();
    expect(screen.getAllByText("Only admins and owners can change integration settings.").length).toBeGreaterThan(0);
  });
});
