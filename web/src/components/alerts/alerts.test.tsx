import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { applyPrefill, createAlertSearch } from "@/lib/alerts";
import { ThemeProvider } from "@/lib/theme";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { MOCK_ALERT_IDS, resetMockAlerts } from "@/mocks/alerts";
import { server } from "@/mocks/server";
import { ChannelsManager } from "./ChannelsManager";
import { RulesList } from "./RulesList";

// uPlot reads window.matchMedia when it loads; jsdom has none.
vi.hoisted(() => {
  if (typeof window !== "undefined" && !window.matchMedia) {
    Object.defineProperty(window, "matchMedia", {
      value: () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }),
    });
  }
});
import { IncidentDetail, IncidentsList } from "./Incidents";
import { MutesManager } from "./MutesManager";
import { RoutingRulesManager } from "./RoutingRulesManager";
import { RuleEditor } from "./RuleEditor";

/** Renders ui inside a memory router (for Links) with a fresh query client and the theme (charts). */
function renderUi(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRootRoute({ component: () => ui });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <RouterProvider router={router as never} />
      </QueryClientProvider>
    </ThemeProvider>,
  );
}

describe("alerting UI", () => {
  beforeEach(() => resetMockAlerts());

  it("rule editor: prefilled metric condition, validation, live preview and save", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    const initial = applyPrefill(createAlertSearch({ metric: "system.cpu.utilization", hostId: "h-1", hostName: "web-1", agg: "avg" }));
    renderUi(<RuleEditor initial={initial} onSaved={onSaved} />);

    expect(await screen.findByLabelText("Metric")).toHaveValue("system.cpu.utilization");
    expect(screen.getByLabelText("Name")).toHaveValue("system.cpu.utilization on web-1");
    expect(screen.getAllByTestId("filter-row")).toHaveLength(2);
    expect(screen.getByLabelText("Across series")).toHaveValue("sum");
    expect(screen.getByText("Complete the condition to see a preview.")).toBeInTheDocument();

    // Submitting without a threshold shows field errors and does not save.
    await user.click(await screen.findByRole("button", { name: "Create rule" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Fix the highlighted fields.");
    expect(screen.getByLabelText("Threshold")).toHaveAttribute("aria-invalid", "true");
    expect(onSaved).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText("Threshold"), "0.9");
    await user.type(screen.getByLabelText("Recovery threshold"), "0.95");
    expect(screen.getByText("Must not be on the breaching side of the threshold.")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Recovery threshold"));
    await user.type(screen.getByLabelText("Recovery threshold"), "0.8");

    // The preview evaluates the draft (debounced) and reports would-fire incidents.
    const summary = await screen.findByTestId("preview-summary", {}, { timeout: 15_000 });
    expect(summary).toHaveTextContent(/Would have opened \d+ incidents? in this range\./);

    await user.click(await screen.findByLabelText(/#ops-alerts/));
    await user.click(screen.getByRole("button", { name: "Create rule" }));
    await vi.waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1), { timeout: 10_000 });
    expect(onSaved.mock.calls[0]![0]).toMatchObject({ name: "system.cpu.utilization on web-1", type: "metric_threshold", channel_ids: [MOCK_ALERT_IDS.channels.slack] });
  }, 40_000);

  it("rule editor: switching type shows the matching condition builder", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<RuleEditor />);
    await user.click(await screen.findByRole("radio", { name: /Log match count/ }));
    expect(screen.getByLabelText("Log text contains")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: /No data/ }));
    expect(screen.getByLabelText("Signal")).toHaveValue("host");
    await user.click(screen.getByRole("radio", { name: /Discovery event/ }));
    expect(screen.getByLabelText("Event")).toBeInTheDocument();
    expect(screen.queryByLabelText("For at least")).not.toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: /^APM(?! service| error)/ }));
    expect(screen.getByLabelText("Service")).toBeInTheDocument();
    expect(screen.getByLabelText("APM metric")).toHaveValue("p95_ms");
  }, 40_000);

  it("incidents: filters, acknowledge, note and resolve", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onFilter = vi.fn();
    const { unmount } = renderUi(<IncidentsList stateFilter="active" onFilterChange={onFilter} />);
    expect(await screen.findAllByTestId("incident-row")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: /Resolved/ }));
    expect(onFilter).toHaveBeenCalledWith({ state: "resolved", severity: undefined });
    unmount();

    renderUi(<IncidentDetail id={MOCK_ALERT_IDS.incidents.cpu} />);
    expect(await screen.findByTestId("incident-summary")).toHaveTextContent("web-1");
    expect(screen.getAllByTestId("delivery-row")).toHaveLength(3);
    await user.click(screen.getByRole("button", { name: "Acknowledge" }));
    expect(await screen.findByText("acknowledged by admin@openlog.local")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Note"), "Restarted the batch job.");
    await user.click(screen.getByRole("button", { name: "Add note" }));
    expect(await screen.findByText("Restarted the batch job.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    await user.type(screen.getByLabelText("Resolution note (optional)"), "fixed");
    await user.click(screen.getByRole("button", { name: "Confirm resolve" }));
    expect(await screen.findByText(/Resolved by admin@openlog.local/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Acknowledge" })).not.toBeInTheDocument();
  }, 40_000);

  it("channels: creates a webhook, reveals the generated secret once and sends tests", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<ChannelsManager />);
    expect(await screen.findAllByTestId("channel-row")).toHaveLength(5);
    expect(screen.getByText("https://hooks.slack.com/…/•••Xk2p")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "New channel" }));
    await user.type(screen.getByLabelText("Name"), "fail hook");
    await user.selectOptions(screen.getByLabelText("Type"), "webhook");
    await user.type(screen.getByLabelText("Webhook URL"), "https://receiver.example.com/fail");
    await user.click(screen.getByRole("button", { name: "Create channel" }));
    const reveal = await screen.findByTestId("secret-reveal");
    expect((within(reveal).getByRole("textbox") as HTMLInputElement).value).toMatch(/^[0-9a-f]{64}$/);
    await user.click(within(reveal).getByRole("button", { name: "Done" }));
    expect(screen.queryByTestId("secret-reveal")).not.toBeInTheDocument();

    const failRow = (await screen.findByText("fail hook")).closest("tr")!;
    await user.click(within(failRow).getByRole("button", { name: "Send test" }));
    expect(await within(failRow).findByRole("alert")).toHaveTextContent("Test failed: HTTP 500 Internal Server Error (status 500).");

    const slackRow = screen.getByText("#ops-alerts").closest("tr")!;
    await user.click(within(slackRow).getByRole("button", { name: "Send test" }));
    expect(await within(slackRow).findByRole("status")).toHaveTextContent(/Test notification delivered \(status 200/);
  }, 40_000);

  it("mutes: creates a mute with a label matcher", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<MutesManager />);
    expect(await screen.findAllByTestId("mute-row")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "New mute" }));
    await user.type(screen.getByLabelText("Name"), "Deploy window");
    await user.click(screen.getByRole("button", { name: "Add matcher" }));
    await user.type(screen.getByLabelText("Label 1"), "host.name");
    await user.type(screen.getByLabelText("Value 1"), "web-1");
    await user.click(screen.getByRole("button", { name: "Create mute" }));
    const row = (await screen.findByText("Deploy window")).closest("tr")!;
    expect(row).toHaveTextContent("host.name equals web-1");
    expect(within(row).getByText("Active")).toBeInTheDocument();
  }, 40_000);

  it("routing rules: ordered list, a new rule and reordering", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<RoutingRulesManager />);
    const rows = await screen.findAllByTestId("routing-rule-row");
    expect(rows).toHaveLength(2);
    // The first match wins, so the order is shown; the default route is always last.
    expect(within(rows[0]!).getByText("Critical to on-call")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("Critical")).toBeInTheDocument(); // the match summary, next to the rule name
    expect(within(rows[0]!).getByText("On-call (PagerDuty)")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Default")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("every incident")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "New routing rule" }));
    await user.type(screen.getByLabelText("Name"), "Warnings to Slack");
    await user.click(screen.getByLabelText(/#ops-alerts/));
    await user.click(screen.getByLabelText("Warning"));
    await user.click(screen.getByRole("button", { name: "Create routing rule" }));

    const created = (await screen.findByText("Warnings to Slack")).closest("tr")!;
    expect(created).toHaveTextContent("Warning");
    expect(created).toHaveTextContent("#ops-alerts");
    expect(screen.getAllByTestId("routing-rule-row")).toHaveLength(3);

    // Moving the second rule up reorders the whole list in one request.
    await user.click(screen.getByRole("button", { name: "Move Warnings to Slack up" }));
    await vi.waitFor(() => expect(screen.getAllByTestId("routing-rule-row")[0]!).toHaveTextContent("Warnings to Slack"));
  }, 40_000);

  it("is read-only for viewers", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID);
    renderUi(
      <>
        <IncidentDetail id={MOCK_ALERT_IDS.incidents.cpu} />
        <ChannelsManager />
        <MutesManager />
      </>,
    );
    expect(await screen.findByText("Your role can view incidents but not change them.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Acknowledge" })).not.toBeInTheDocument();
    expect(screen.getByText("Only admins can create, change or test channels.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New channel" })).not.toBeInTheDocument();
    expect(await screen.findByText("Your role can view mutes but not change them.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send test" })).not.toBeInTheDocument();
  }, 40_000);
});

describe("RulesList", () => {
  it("sorts by name and puts the most severe first on the first click of its column", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    resetMockAlerts();
    const user = userEvent.setup();
    renderUi(<RulesList />);
    await screen.findAllByTestId("rule-row");
    const names = () => screen.getAllByTestId("rule-row").map((r) => within(r).getAllByRole("cell")[0]!.textContent!);

    const initial = names();
    expect(initial).toEqual([...initial].sort((a, b) => a.localeCompare(b)));

    await user.click(screen.getByRole("button", { name: "Severity" }));
    const severities = screen.getAllByTestId("rule-row").map((r) => within(r).getAllByRole("cell")[2]!.textContent);
    expect(severities[0]).toBe("Critical");
    expect(severities[severities.length - 1]).not.toBe("Critical");
    expect(screen.getByRole("columnheader", { name: "Severity" })).toHaveAttribute("aria-sort", "descending");
  });
});

describe("the incidents list", () => {
  it("reaches the rest with the next cursor, keeping the order and the filters", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const seen: URLSearchParams[] = [];
    const incident = (id: string, summary: string) => ({
      id, rule_id: null, rule_name: "rule", state: "open", severity: "critical", muted: false, flapping: false, summary, labels: {}, value: null, threshold: null, last_value: null,
      opened_at: "2026-10-01T10:00:00.000000000Z", resolved_at: null, acknowledged_at: null, acknowledged_by_email: null, resolved_by_email: null, resolve_reason: null,
    });
    server.use(
      http.get("*/api/v1/alerts/incidents", ({ request }) => {
        const p = new URL(request.url).searchParams;
        seen.push(p);
        const counts = { open: 2, acknowledged: 0, resolved: 0 };
        return HttpResponse.json(p.get("cursor") === "page-2" ? { incidents: [incident("b", "the older one")], next_cursor: null, counts } : { incidents: [incident("a", "the newer one")], next_cursor: "page-2", counts });
      }),
    );
    renderUi(<IncidentsList stateFilter="active" onFilterChange={vi.fn()} />);
    await screen.findByText("the newer one");
    expect(seen[0]!.get("cursor")).toBeNull();
    expect(seen[0]!.has("offset")).toBe(false);

    await user.click(screen.getByRole("button", { name: "Load more incidents" }));
    await screen.findByText("the older one");
    const last = seen[seen.length - 1]!;
    expect(last.get("cursor")).toBe("page-2");
    expect(last.get("state")).toBe("open,acknowledged");
    expect(last.has("offset")).toBe(false);
    // The second page goes under the first, and there is no third to ask for.
    const rows = screen.getAllByTestId("incident-row").map((r) => r.textContent);
    expect(rows[0]).toContain("the newer one");
    expect(rows[1]).toContain("the older one");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Load more incidents" })).not.toBeInTheDocument());
  });

  it("keeps what it has when the next page fails, and can try again", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const incident = { id: "a", rule_id: null, rule_name: "rule", state: "open", severity: "critical", muted: false, flapping: false, summary: "the newer one", labels: {}, value: null, threshold: null, last_value: null, opened_at: "2026-10-01T10:00:00.000000000Z", resolved_at: null };
    server.use(
      http.get("*/api/v1/alerts/incidents", ({ request }) =>
        new URL(request.url).searchParams.get("cursor")
          ? HttpResponse.json({ error: { code: "internal", message: "broken" } }, { status: 500 })
          : HttpResponse.json({ incidents: [incident], next_cursor: "page-2", counts: { open: 1, acknowledged: 0, resolved: 0 } }),
      ),
    );
    renderUi(<IncidentsList stateFilter="active" onFilterChange={vi.fn()} />);
    await screen.findByText("the newer one");
    await user.click(screen.getByRole("button", { name: "Load more incidents" }));
    await screen.findByRole("alert");
    expect(screen.getByText("the newer one")).toBeInTheDocument();
  });
});
