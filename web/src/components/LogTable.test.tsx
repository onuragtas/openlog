import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LogRecord } from "@/api/types";
import { LogTable } from "@/components/LogTable";
import { stubLayout } from "@/test/layout";

const t0 = Date.UTC(2026, 8, 14, 10, 0, 0);
const logs: LogRecord[] = Array.from({ length: 5000 }, (_, i) => ({
  timestamp: new Date(t0 - i * 1000).toISOString(),
  severity_text: i % 10 === 0 ? "ERROR" : "INFO",
  severity_number: i % 10 === 0 ? 17 : 9,
  body: `request ${i} handled`,
  host_id: "h1",
  service_name: "checkout",
  trace_id: "",
  span_id: "",
  attributes: { "http.route": `/r/${i}` },
  resource_attributes: { "service.name": "checkout" },
}));

describe("LogTable virtualization", () => {
  beforeEach(() => stubLayout({ viewportHeight: 600, width: 1200, itemHeight: 33 }));
  afterEach(() => vi.restoreAllMocks());

  it("renders only the rows in view out of thousands", () => {
    render(<LogTable logs={logs} />);
    const rows = within(screen.getByTestId("log-scroll")).getAllByRole("row");
    expect(rows.length).toBeGreaterThan(10);
    expect(rows.length).toBeLessThan(80);
    expect(screen.getByRole("table")).toHaveAttribute("aria-rowcount", "5001");
    expect(rows[0]).toHaveTextContent("request 0 handled");
  });

  it("expands rows from the keyboard and loads more pages", async () => {
    const user = userEvent.setup();
    const onLoadMore = vi.fn();
    render(<LogTable logs={logs.slice(0, 300)} hasMore onLoadMore={onLoadMore} />);
    const first = within(screen.getByTestId("log-scroll")).getAllByRole("row")[0]!;
    const expand = within(first).getByRole("button", { name: /expand/i });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    expand.focus();
    await user.keyboard("{Enter}");
    const row = within(screen.getByTestId("log-scroll")).getAllByRole("row")[0]!;
    expect(within(row).getByRole("button", { name: /collapse/i })).toHaveAttribute("aria-expanded", "true");
    expect(row).toHaveTextContent("/r/0");
    await user.click(screen.getByRole("button", { name: "Load older logs" }));
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });
});
