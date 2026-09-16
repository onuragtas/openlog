import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { MOCK_SLO_IDS, resetMockSlos } from "@/mocks/slos";
import { SloDetail } from "./SloDetail";
import { SloForm } from "./SloForm";
import { SloList } from "./SloList";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(() => resetMockSlos());

describe("SLOs", () => {
  it("lists objectives with their error budget and opens one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    renderWithClient(<SloList onOpen={onOpen} canWrite />);

    const list = await screen.findByTestId("slo-list");
    expect(within(list).getByText("Checkout availability")).toBeInTheDocument();
    expect(within(list).getByText("Catalog latency")).toBeInTheDocument();
    // The availability SLO has spent more than its budget (1.2 ‰ bad against a 1 ‰ budget).
    expect(within(list).getByText("Budget exhausted")).toBeInTheDocument();
    expect(within(list).getAllByTestId("budget-bar")).toHaveLength(2);
    expect(within(list).getByText("99.9%")).toBeInTheDocument();

    await user.click(within(list).getByRole("button", { name: "Checkout availability" }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: MOCK_SLO_IDS.availability }));
  });

  it("creates an objective and rejects an impossible target", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithClient(<SloForm onSaved={onSaved} />);

    await user.type(screen.getByLabelText("Name"), "Orders availability");
    await user.clear(screen.getByLabelText("Objective (%)"));
    await user.type(screen.getByLabelText("Objective (%)"), "100");
    await user.click(screen.getByRole("button", { name: "Create SLO" }));
    expect(screen.getByText("Enter a percentage of at least 50 and below 100.")).toBeInTheDocument();
    expect(screen.getByText("Required.")).toBeInTheDocument(); // the service is still empty
    expect(onSaved).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText("Service"), "orders");
    await user.clear(screen.getByLabelText("Objective (%)"));
    await user.type(screen.getByLabelText("Objective (%)"), "99.5");
    await user.click(screen.getByRole("button", { name: "Create SLO" }));

    await vi.waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(onSaved.mock.calls[0]![0]).toMatchObject({ name: "Orders availability", service_name: "orders", objective: 99.5, window_days: 28 });
  });

  it("shows the budget, the burndown and the burn windows of an objective", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<SloDetail id={MOCK_SLO_IDS.availability} canWrite />);

    expect(await screen.findByTestId("slo-detail")).toBeInTheDocument();
    expect(screen.getByTestId("burndown")).toBeInTheDocument();
    const windows = screen.getByTestId("burn-windows");
    expect(within(windows).getByText("Fast (1 h / 5 m)")).toBeInTheDocument();
    // The fast window burns 18.2× against its 14.4× threshold, the slow one stays inside its budget.
    expect(within(windows).getByText("Burning")).toBeInTheDocument();
    expect(within(windows).getByText("Within budget")).toBeInTheDocument();
    expect(within(windows).getByText("14.4×")).toBeInTheDocument();
  });
});
