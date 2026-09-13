import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { resetMockApm } from "@/mocks/apm";
import { ApdexSettings } from "./ApdexSettings";
import { LatencyHistogram, RedTiles, Sparkline } from "./Charts";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(() => resetMockApm());

describe("APM charts", () => {
  it("renders RED tiles with formatted values and an Apdex rating", () => {
    render(<RedTiles red={{ requests: 600, throughput: 10, errors: 6, error_rate: 0.01, avg_ms: 42.4, p50_ms: 20, p95_ms: 1480, p99_ms: null, apdex: 0.82 }} apdexTMs={500} />);
    const tiles = screen.getByTestId("red-tiles");
    expect(within(tiles).getByText("10 rpm")).toBeInTheDocument();
    expect(within(tiles).getByText("1%")).toBeInTheDocument();
    expect(within(tiles).getByText("1.48 s")).toBeInTheDocument();
    expect(within(tiles).getByText("–")).toBeInTheDocument();
    expect(within(tiles).getByText("0.82")).toBeInTheDocument();
    expect(within(tiles).getByText("Fair · T = 500 ms")).toBeInTheDocument();
  });

  it("labels histogram bars and sparklines for assistive technology", () => {
    render(
      <>
        <LatencyHistogram bins={[{ from_ms: 2 ** (79 / 8), to_ms: 1024, count: 12 }, { from_ms: 2 ** (81 / 8), to_ms: 2 ** (82 / 8), count: 3 }]} />
        <Sparkline points={[[0, 1], [60_000, 3]]} label="Throughput trend" />
        <Sparkline points={[]} label="empty" />
      </>,
    );
    const bars = within(screen.getByTestId("latency-histogram")).getAllByRole("listitem");
    expect(bars).toHaveLength(3); // the empty bucket between the two is filled in
    expect(bars[0]).toHaveTextContent("12 requests between 939 ms and 1.02 s");
    expect(screen.getByRole("img", { name: "Throughput trend" })).toBeInTheDocument();
  });
});

describe("ApdexSettings", () => {
  it("lets an admin change Apdex T", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<ApdexSettings scope={{ service: "orders", namespace: "shop", environment: "prod" }} />);
    expect(await screen.findByText("T = 500 ms")).toBeInTheDocument();
    expect(screen.getByText("(default)")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Edit Apdex T" }));
    const input = screen.getByLabelText("Apdex T (ms)");
    await user.clear(input);
    await user.type(input, "0");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Enter a whole number between 1 and 600000.");

    await user.clear(input);
    await user.type(input, "300");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("T = 300 ms")).toBeInTheDocument();
    expect(screen.queryByText("(default)")).not.toBeInTheDocument();
  });

  it("hides the editor from viewers", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // the mock user is a viewer there
    renderWithClient(<ApdexSettings scope={{ service: "orders" }} />);
    expect(await screen.findByText("T = 500 ms")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit Apdex T" })).not.toBeInTheDocument();
  });
});
