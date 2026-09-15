import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { NoHostLogs } from "./logs-tab";

// The tab embeds the Logs Explorer, whose histogram needs uPlot (matchMedia/canvas); charts have their own tests.
vi.mock("@/components/TimeSeriesChart", () => ({ TimeSeriesChart: () => null }));

describe("host logs per OS (D-112)", () => {
  it("Linux: journald config and systemctl restart", () => {
    render(<NoHostLogs os="linux" />);
    expect(screen.getByTestId("host-logs-config")).toHaveTextContent("journald:");
    expect(screen.getByTestId("host-logs-config")).toHaveAttribute("data-lang", "yaml");
    expect(screen.getByTestId("host-logs-restart")).toHaveTextContent("sudo systemctl restart openlog-infra-agent");
  });

  it("macOS: unified log predicate, Homebrew log path and launchctl", () => {
    render(<NoHostLogs os="darwin" />);
    const empty = screen.getByTestId("host-logs-empty");
    expect(empty).toHaveTextContent("/etc/openlog-infra-agent/config.yaml has a logs section");
    expect(screen.getByTestId("host-logs-config")).toHaveTextContent("unified_log:");
    expect(screen.getByTestId("host-logs-config")).toHaveTextContent("predicate:");
    expect(screen.getByTestId("host-logs-config")).toHaveTextContent("/opt/homebrew/var/log/nginx/*.log");
    expect(screen.getByTestId("host-logs-restart")).toHaveTextContent("sudo launchctl kickstart -k system/org.openlog.infra-agent");
    expect(empty.textContent).not.toMatch(/journald|systemctl/);
  });

  it("Windows: ProgramData path, Event Log channels, IIS logs and PowerShell restart", () => {
    render(<NoHostLogs os="windows" />);
    const empty = screen.getByTestId("host-logs-empty");
    expect(empty).toHaveTextContent("C:\\ProgramData\\openlog\\infra-agent\\config.yaml has a logs section");
    expect(screen.getByTestId("host-logs-config")).toHaveTextContent("windows_event_log:");
    expect(screen.getByTestId("host-logs-config")).toHaveTextContent("C:\\inetpub\\logs\\LogFiles\\W3SVC1\\*.log");
    expect(screen.getByTestId("host-logs-restart")).toHaveTextContent("Restart-Service openlog-infra-agent");
    expect(screen.getByTestId("host-logs-restart")).toHaveAttribute("data-lang", "powershell");
    expect(empty.textContent).not.toMatch(/journald|systemctl|sudo/);
  });
});
