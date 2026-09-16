import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { MOCK_SYNTHETIC_IDS, resetMockSynthetics } from "@/mocks/synthetics";
import { SyntheticDetail } from "./SyntheticDetail";
import { SyntheticForm } from "./SyntheticForm";
import { SyntheticsList } from "./SyntheticsList";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(async () => {
  resetMockSynthetics();
  await i18n.changeLanguage("en");
});

describe("Synthetics", () => {
  it("lists checks with their state and uptime, and opens one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    renderWithClient(<SyntheticsList onOpen={onOpen} canWrite />);

    const list = await screen.findByTestId("synthetics-list");
    expect(within(list).getByText("Checkout health")).toBeInTheDocument();
    expect(within(list).getByText("Catalog API")).toBeInTheDocument();
    // The catalog check's last run returned 503, the checkout check is healthy.
    expect(within(list).getByText("Up")).toBeInTheDocument();
    expect(within(list).getByText("Down")).toBeInTheDocument();
    // Each row carries the latency sparkline of the last 24 hours.
    expect(within(list).getAllByTestId("sparkline")).toHaveLength(2);

    await user.click(within(list).getByRole("button", { name: "Checkout health" }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: MOCK_SYNTHETIC_IDS.up }));
  });

  it("creates a check and rejects an unusable URL", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithClient(<SyntheticForm onSaved={onSaved} />);

    await user.type(screen.getByLabelText("Name"), "Orders health");
    await user.type(screen.getByLabelText("URL"), "ftp://example.com");
    await user.click(screen.getByRole("button", { name: "Create check" }));
    expect(screen.getByText("Enter an http or https URL without credentials.")).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();

    await user.clear(screen.getByLabelText("URL"));
    await user.type(screen.getByLabelText("URL"), "https://shop.example.com/orders");
    await user.click(screen.getByRole("button", { name: "Create check" }));

    await vi.waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(onSaved.mock.calls[0]![0]).toMatchObject({
      name: "Orders health",
      url: "https://shop.example.com/orders",
      method: "GET",
      enabled: true,
      interval_seconds: 300,
    });
  });

  it("rejects a timeout longer than the interval", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithClient(<SyntheticForm onSaved={onSaved} />);

    await user.type(screen.getByLabelText("Name"), "Slow check");
    await user.type(screen.getByLabelText("URL"), "https://shop.example.com/slow");
    await user.clear(screen.getByLabelText("Interval (seconds)"));
    await user.type(screen.getByLabelText("Interval (seconds)"), "30");
    await user.clear(screen.getByLabelText("Timeout (ms)"));
    await user.type(screen.getByLabelText("Timeout (ms)"), "60000");
    await user.click(screen.getByRole("button", { name: "Create check" }));

    expect(screen.getByText("Enter 500 to 60000 milliseconds, not longer than the interval.")).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("shows uptime, latency and the recent failures of a check", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<SyntheticDetail id={MOCK_SYNTHETIC_IDS.down} canWrite />);

    expect(await screen.findByTestId("synthetic-detail")).toBeInTheDocument();
    expect(screen.getByTestId("synthetic-tiles")).toBeInTheDocument();
    const failures = screen.getByTestId("synthetic-failures");
    expect(within(failures).getAllByText("HTTP 503, expected 200").length).toBeGreaterThan(0);
    // The failure kind is translated, not shown as the raw enum value.
    expect(within(failures).getAllByText("Status code").length).toBeGreaterThan(0);
    // The schedule row of the built-in location is named.
    expect(within(screen.getByTestId("synthetic-locations")).getByText("openlog server")).toBeInTheDocument();
  });

  it("Turkish check states and failure kinds", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("tr");
    renderWithClient(<SyntheticsList onOpen={vi.fn()} />);

    const list = await screen.findByTestId("synthetics-list");
    expect(within(list).getByText("Çalışıyor")).toBeInTheDocument();
    expect(within(list).getByText("Çalışmıyor")).toBeInTheDocument();
    expect(within(list).getByText("Çalışma oranı")).toBeInTheDocument();
  });
});
