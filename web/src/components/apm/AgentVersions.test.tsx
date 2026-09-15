import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import type { ApmServiceAgents } from "@/api/apm";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { agentRows } from "@/lib/agent-versions";
import { AgentUpgradeNotice, AgentVersionBadge, AgentVersionsTable } from "./AgentVersions";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

const seen = "2026-09-15T12:00:00.000000000Z";

const php: ApmServiceAgents = {
  service_name: "catalog",
  service_namespace: "shop",
  environment: "prod",
  status: "unsupported",
  agents: [
    {
      kind: "php",
      distro_name: "openlog-php",
      sdk_name: "",
      sdk_language: "php",
      status: "unsupported",
      instances: 2,
      last_seen: seen,
      versions: [{ version: "0.1.9", status: "unsupported", instances: 2, spans: 20, last_seen: seen }],
      versions_truncated: false,
      instrumentation_modules: [],
      upgrade: null,
    },
  ],
};

afterEach(() => localStorage.clear());

describe("AgentVersionBadge", () => {
  it("shows running and latest version for an agent that needs an upgrade", () => {
    render(<AgentVersionBadge service={php} latest="0.1.31" />);
    const badge = screen.getByTestId("agent-version-badge");
    expect(badge).toHaveAttribute("title", "openlog-php 0.1.9 · latest 0.1.31");
    expect(badge).toHaveTextContent("Agent unsupported");
  });

  it("renders nothing without a problem or a latest version", () => {
    const { container } = render(
      <>
        <AgentVersionBadge service={php} latest={null} />
        <AgentVersionBadge service={undefined} latest="0.1.31" />
      </>,
    );
    expect(container).toBeEmptyDOMElement();
  });
});

describe("AgentUpgradeNotice", () => {
  it("shows versions and the upgrade command and stays dismissed for this release", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const scope = { service: "frontend", namespace: "shop", environment: "prod" };
    const first = renderWithClient(<AgentUpgradeNotice scope={scope} range={{}} />);
    const notice = await screen.findByRole("region", { name: "openlog-node is outdated" });
    expect(within(notice).getByTestId("agent-running-versions")).toHaveTextContent("0.1.28");
    expect(within(notice).getByTestId("agent-upgrade-command")).toHaveTextContent("npm install openlog-node@0.1.31");
    expect(within(notice).getByText(/does not update language agents by itself/)).toBeInTheDocument();

    await user.click(within(notice).getByRole("button", { name: "Dismiss until the next release" }));
    expect(screen.queryByTestId("agent-upgrade-notice")).not.toBeInTheDocument();
    first.unmount();

    renderWithClient(<AgentUpgradeNotice scope={scope} range={{}} />);
    renderWithClient(<AgentUpgradeNotice scope={{ service: "catalog", namespace: "shop", environment: "prod" }} range={{}} />);
    expect(await screen.findByRole("region", { name: "openlog-php is no longer supported" })).toBeInTheDocument();
    expect(screen.getAllByTestId("agent-upgrade-notice")).toHaveLength(1);
  });

  it("stays hidden for an up-to-date agent", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<AgentUpgradeNotice scope={{ service: "orders", namespace: "shop", environment: "prod" }} range={{}} />);
    renderWithClient(<AgentUpgradeNotice scope={{ service: "catalog", namespace: "shop", environment: "prod" }} range={{}} />);
    await screen.findByRole("region", { name: "openlog-php is no longer supported" });
    expect(screen.getAllByTestId("agent-upgrade-notice")).toHaveLength(1);
  });
});

describe("AgentVersionsTable", () => {
  it("lists every version with its status", () => {
    render(<AgentVersionsTable rows={agentRows([php])} />);
    const row = within(screen.getByTestId("agent-versions")).getAllByRole("row")[1];
    expect(row).toHaveTextContent("catalog");
    expect(row).toHaveTextContent("openlog-php");
    expect(row).toHaveTextContent("0.1.9");
    expect(row).toHaveTextContent("Unsupported");
  });
});
