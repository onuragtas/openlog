import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";
import { QueryBuilder } from "@/components/querybuilder/QueryBuilder";
import { server } from "@/mocks/server";
import { CellMenu } from "./DataTable";

describe("CellMenu", () => {
  it("filters in, filters out, toggles the column and never bubbles to the row", async () => {
    const user = userEvent.setup();
    const actions = { onFilter: vi.fn(), onToggleColumn: vi.fn(), canFilter: true };
    const onRow = vi.fn();
    render(
      <div onClick={onRow}>
        <CellMenu column="attributes.http.route" label="attributes.http.route" value="/api/cart" actions={actions} isColumn />
      </div>,
    );
    const open = () => user.click(screen.getByRole("button", { name: "Value actions: attributes.http.route" }));
    await open();
    await user.click(screen.getByRole("button", { name: "Filter for this value" }));
    await open();
    await user.click(screen.getByRole("button", { name: "Exclude this value" }));
    await open();
    await user.click(screen.getByRole("button", { name: "Remove column" }));
    expect(actions.onFilter.mock.calls).toEqual([
      ["attributes.http.route", "/api/cart", false],
      ["attributes.http.route", "/api/cart", true],
    ]);
    expect(actions.onToggleColumn).toHaveBeenCalledWith("attributes.http.route");
    expect(onRow).not.toHaveBeenCalled();
  });

  it("disables filters at the condition limit and for values over 1024 bytes", async () => {
    const user = userEvent.setup();
    const actions = { onFilter: vi.fn(), onToggleColumn: vi.fn(), canFilter: true };
    const { rerender } = render(<CellMenu column="body" label="Body" value={"x".repeat(1025)} actions={actions} isColumn={false} />);
    await user.click(screen.getByRole("button", { name: "Value actions: Body" }));
    expect(screen.getByRole("button", { name: "Filter for this value" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Add as column" })).toBeEnabled();
    rerender(<CellMenu column="body" label="Body" value="short" actions={{ ...actions, canFilter: false }} isColumn={false} />);
    expect(screen.getByRole("button", { name: "Exclude this value" })).toBeDisabled();
  });
});

describe("QueryBuilder locked context", () => {
  it("shows locked conditions that cannot be removed and sends them with value suggestions", async () => {
    const valueRequests: URL[] = [];
    server.use(
      http.get("*/api/v1/fields/keys", () =>
        HttpResponse.json({ keys: [{ key: "severity_text", name: "severity_text", source: "field", type: "string", count: null, cardinality: null }], sampled: false }),
      ),
      http.get("*/api/v1/fields/values", ({ request }) => {
        valueRequests.push(new URL(request.url));
        return HttpResponse.json({ key: "severity_text", type: "string", values: [{ value: "ERROR", count: 3 }], total: 3, sampled: false });
      }),
    );
    const user = userEvent.setup();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <QueryBuilder signal="logs" range={{ range: "1h" }} value={{ filters: [], groups: [], q: "" }} onChange={() => {}} locked={[{ key: "host.id", op: "=", value: "h-1" }]} />
      </QueryClientProvider>,
    );
    const chip = screen.getByTestId("locked-chip");
    expect(chip).toHaveTextContent("host.id=h-1");
    expect(screen.getByText("Always applied on this page: host.id = h-1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Remove condition host\.id/ })).toBeNull();
    const input = screen.getByRole("combobox");
    await user.click(input);
    await user.click(await screen.findByRole("option", { name: /severity_text/ }));
    await user.click(await screen.findByRole("option", { name: /^=/ }));
    await screen.findByRole("option", { name: /ERROR/ });
    const filters = JSON.parse(valueRequests.at(-1)!.searchParams.get("filters")!) as unknown[];
    expect(filters).toEqual([{ key: "host.id", op: "=", value: "h-1" }]);
  });
});
