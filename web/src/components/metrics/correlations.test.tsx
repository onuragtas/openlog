import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { correlationWindow } from "@/components/alerts/Incidents";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { Correlations, formatChange, scoreVariant, seriesLabel } from "./Correlations";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const WINDOW = { from: "2026-09-21T10:00:00Z", to: "2026-09-21T10:12:00Z" };

describe("metric correlation", () => {
  it("ranks the series that moved, strongest first, showing both means", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<Correlations {...WINDOW} />);

    const table = await screen.findByTestId("correlations");
    const rows = within(table).getAllByTestId("correlation-row");
    // The server orders by score; the panel must not reorder it.
    expect(rows[0]).toHaveTextContent("system.disk.operation_time");
    expect(rows[1]).toHaveTextContent("system.disk.pending_operations");
    // Both means are printed, because the score is derived from them.
    expect(rows[1]).toHaveTextContent("0.8");
    expect(rows[1]).toHaveTextContent("41.2");
    expect(rows[1]).toHaveTextContent("19.1");
    expect(screen.getByText("1,842 series compared against their own baseline.")).toBeInTheDocument();
  });

  it("narrows to one host when the incident names one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<Correlations {...WINDOW} hostId="web-1" />);

    const table = await screen.findByTestId("correlations");
    const rows = within(table).getAllByTestId("correlation-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent("http.server.request.duration");
  });

  it("prints no ratio when the baseline was zero, rather than an infinite one", () => {
    expect(formatChange(null)).toBe("–");
    expect(formatChange(Number.POSITIVE_INFINITY)).toBe("–");
    expect(formatChange(18.62, "en")).toBe("+1,862%");
    expect(formatChange(-0.69, "en")).toBe("-69%");
  });

  it("labels a series by its attributes, without openlog's own bookkeeping", () => {
    expect(seriesLabel({ attributes: { device: "nvme0n1", direction: "write", "openlog.shard": "3" } })).toBe("device=nvme0n1 · direction=write");
    expect(seriesLabel({ attributes: {} })).toBe("");
  });

  it("gets louder with the score", () => {
    expect(scoreVariant(0.5)).toBe("muted");
    expect(scoreVariant(4)).toBe("warning");
    expect(scoreVariant(27.4)).toBe("destructive");
  });
});

describe("correlationWindow", () => {
  const opened = "2026-09-21T10:00:00Z";
  const at = (s: string) => Date.parse(s);

  it("spans the incident when it is over", () => {
    expect(correlationWindow(opened, "2026-09-21T10:12:00Z", at("2026-09-21T12:00:00Z"))).toEqual({
      from: "2026-09-21T10:00:00.000Z",
      to: "2026-09-21T10:12:00.000Z",
    });
  });

  it("runs to now while the incident is open", () => {
    expect(correlationWindow(opened, null, at("2026-09-21T10:20:00Z")).to).toBe("2026-09-21T10:20:00.000Z");
  });

  it("keeps five minutes even for an incident that opened and resolved at once, so there are buckets to compare", () => {
    expect(correlationWindow(opened, "2026-09-21T10:00:30Z", at("2026-09-21T11:00:00Z")).to).toBe("2026-09-21T10:05:00.000Z");
  });

  it("caps a long incident at its first hour: the mean over three days is not 'during'", () => {
    expect(correlationWindow(opened, null, at("2026-09-24T10:00:00Z")).to).toBe("2026-09-21T11:00:00.000Z");
  });
});
