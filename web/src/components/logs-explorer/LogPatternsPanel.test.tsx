import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";
import type { FilterState } from "@/api/explorer";
import { server } from "@/mocks/server";
import { LogPatternsPanel } from "./LogPatternsPanel";

const severity = (over: Partial<Record<"unspecified" | "trace" | "debug" | "info" | "warn" | "error" | "fatal", number>> = {}) => ({
  unspecified: 0,
  trace: 0,
  debug: 0,
  info: 0,
  warn: 0,
  error: 0,
  fatal: 0,
  ...over,
});

const RESPONSE = {
  patterns: [
    {
      pattern_id: "10748583442455546363",
      template: "user <*> logged in from <*>",
      count: 75,
      severity: severity({ info: 75 }),
      max_severity_number: 9,
      services: ["checkout", "auth"],
      first_seen: "2026-09-16T11:00:00.000000000Z",
      last_seen: "2026-09-16T12:00:00.000000000Z",
      sample: {
        timestamp: "2026-09-16T12:00:00.000000000Z",
        body: "user 4711 logged in from 10.0.0.3",
        service_name: "checkout",
        severity_text: "INFO",
        severity_number: 9,
        trace_id: "",
      },
    },
    {
      pattern_id: "55",
      template: "upstream request failed: <*>",
      count: 25,
      severity: severity({ error: 25 }),
      max_severity_number: 17,
      services: ["payments"],
      first_seen: "2026-09-16T11:30:00.000000000Z",
      last_seen: "2026-09-16T12:00:00.000000000Z",
      sample: {
        timestamp: "2026-09-16T12:00:00.000000000Z",
        body: "upstream request failed: connection timeout",
        service_name: "payments",
        severity_text: "ERROR",
        severity_number: 17,
        trace_id: "",
      },
    },
  ],
  total: 100,
  unclassified: 0,
  rollup: false,
  truncated: false,
};

const FILTER: FilterState = { filters: [{ key: "service.name", op: "=", value: "checkout" }], groups: [[{ key: "host.id", op: "exists" }]], q: "timeout" };

function renderPanel(props: Partial<Parameters<typeof LogPatternsPanel>[0]> = {}, data: Record<string, unknown> = RESPONSE) {
  const bodies: Record<string, unknown>[] = [];
  server.use(
    http.post("*/api/v1/logs/patterns", async ({ request }) => {
      bodies.push((await request.json()) as Record<string, unknown>);
      return HttpResponse.json(data);
    }),
  );
  const onSelect = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <LogPatternsPanel
        range={{ range: "1h" }}
        filter={FILTER}
        context={{ transaction: "GET /cart", transactionService: "checkout" }}
        canFilter
        onSelect={onSelect}
        {...props}
      />
    </QueryClientProvider>,
  );
  return { bodies, onSelect };
}

describe("LogPatternsPanel", () => {
  it("lists the templates with their record count and share of volume", async () => {
    renderPanel();
    const items = await screen.findAllByTestId("log-pattern");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("user <*> logged in from <*>");
    expect(items[0]).toHaveTextContent("75 records");
    expect(items[0]).toHaveTextContent("75%");
    // The sample record and the services of the pattern are shown next to the template.
    expect(items[0]).toHaveTextContent("user 4711 logged in from 10.0.0.3");
    expect(items[0]).toHaveTextContent("checkout, auth");
    // The severity badge comes from the highest severity in the pattern.
    expect(items[1]).toHaveTextContent("ERROR");
    expect(items[1]).toHaveTextContent("25%");
    expect(screen.getByText("2 patterns in 100 records")).toBeInTheDocument();
  });

  it("sends the explorer conditions with the request", async () => {
    const { bodies } = renderPanel();
    await screen.findAllByTestId("log-pattern");
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({
      filters: [{ key: "service.name", op: "=", value: "checkout" }],
      groups: [[{ key: "host.id", op: "exists" }]],
      q: "timeout",
      transaction: "GET /cart",
      transaction_service: "checkout",
    });
  });

  it("selects a pattern to filter the record list by", async () => {
    const user = userEvent.setup();
    const { onSelect } = renderPanel();
    const items = await screen.findAllByTestId("log-pattern");
    await user.click(within(items[1]!).getByRole("button", { name: /upstream request failed/ }));
    expect(onSelect).toHaveBeenCalledWith("55");
  });

  it("cannot select a pattern at the condition limit", async () => {
    const user = userEvent.setup();
    const { onSelect } = renderPanel({ canFilter: false });
    const items = await screen.findAllByTestId("log-pattern");
    const button = within(items[0]!).getByRole("button", { name: /user <\*> logged in/ });
    expect(button).toBeDisabled();
    await user.click(button);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("explains records without a pattern and hour-aligned rollup counts", async () => {
    renderPanel({}, { ...RESPONSE, unclassified: 12, rollup: true, truncated: true });
    await screen.findAllByTestId("log-pattern");
    expect(screen.getByText(/12 records have no pattern/)).toBeInTheDocument();
    expect(screen.getByText(/cover whole hours/)).toBeInTheDocument();
    expect(screen.getByText(/2 largest patterns/)).toBeInTheDocument();
  });

  it("shows an empty state when nothing matches", async () => {
    renderPanel({}, { patterns: [], total: 0, unclassified: 0, rollup: false, truncated: false });
    expect(await screen.findByText("No patterns match these filters in the selected range.")).toBeInTheDocument();
  });
});
