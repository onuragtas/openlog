import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { FleetHost, FleetHostJavaAgent } from "@/api/fleet";
import { hasJavaAgent } from "@/lib/java-agent";
import { JavaAgentPanel } from "./JavaAgentPanel";

const base: FleetHostJavaAgent = {
  reported: true,
  mode: "auto",
  agent_mode: "auto",
  source: "remote",
  capable: true,
  reason: "",
  managed: true,
  version: "0.4.0",
  state: "restart_pending",
  detail: "1 JVM(s) still run an older Java agent: restart the application(s) to load 0.4.0",
  link_path: "/opt/openlog/openlog-javaagent.jar",
  link_state: "managed",
  jvms: [
    { pid: 4242, name: "java", command: "java -jar shop.jar", agent_path: "/opt/openlog/openlog-javaagent.jar", loaded_version: "0.3.0", managed: true,
      restart_pending: true, started_at: "2026-09-15T08:00:00Z", container: false },
    { pid: 4343, name: "java", command: "java -jar api.jar", agent_path: "/opt/openlog/openlog-javaagent.jar", loaded_version: "0.4.0", managed: true,
      restart_pending: false, started_at: "2026-09-15T09:00:00Z", container: false },
    { pid: 99, name: "java", command: "java -jar legacy.jar", agent_path: "/srv/openlog-javaagent-0.2.0.jar", loaded_version: "", managed: false,
      restart_pending: false, started_at: "", container: false },
  ],
  update: { operation: "upgrade", version: "0.4.0", state: "applied", error: "", changed_at: "2026-09-15T09:30:00Z" },
  override: null,
  status: "up_to_date",
  status_target: "0.4.0",
};

function host(java: FleetHostJavaAgent): FleetHost {
  return { host_id: "h1", host_name: "app-1", java_agent: java } as unknown as FleetHost;
}

function renderPanel(java: FleetHostJavaAgent, canManage = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <JavaAgentPanel host={host(java)} canManage={canManage} />
    </QueryClientProvider>,
  );
}

describe("JavaAgentPanel", () => {
  it("lists JVMs with the loaded version and restart pending badges", () => {
    renderPanel(base, true);
    const rows = screen.getAllByTestId("java-jvm-row");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]!).getByText("0.3.0")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("Restart pending")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Current")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Unknown")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Not managed")).toBeInTheDocument();
    expect(screen.getByTestId("java-restart-hint")).toHaveTextContent("Restart these applications to load Java agent 0.4.0");
    expect(screen.getByLabelText("Java agent mode on this host")).toHaveValue("");
  });

  it("explains an unmanaged jar and hides the mode for viewers", () => {
    renderPanel({ ...base, managed: false, version: null, state: "unmanaged", link_state: "unmanaged", detail: "left alone", jvms: [], update: null });
    expect(screen.getByText("Not managed")).toBeInTheDocument();
    expect(screen.getByTestId("java-agent-detail")).toHaveTextContent("left alone");
    expect(screen.queryByTestId("java-restart-hint")).toBeNull();
    expect(screen.queryByLabelText("Java agent mode on this host")).toBeNull();
  });

  it("renders nothing for hosts without Java", () => {
    const none = { ...base, managed: false, version: null, state: "not_found", jvms: [], update: null };
    expect(hasJavaAgent(none)).toBe(false);
    expect(hasJavaAgent({ ...base, reported: false })).toBe(false);
    const { container } = renderPanel(none);
    expect(container).toBeEmptyDOMElement();
  });
});
