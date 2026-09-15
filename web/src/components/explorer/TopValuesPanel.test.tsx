import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";
import { server } from "@/mocks/server";
import { TopValuesPanel } from "./TopValuesPanel";

function renderPanel(props: Partial<Parameters<typeof TopValuesPanel>[0]> = {}) {
  const requests: URL[] = [];
  server.use(
    http.get("*/api/v1/fields/values", ({ request }) => {
      const url = new URL(request.url);
      requests.push(url);
      return HttpResponse.json({
        key: url.searchParams.get("key"),
        type: "string",
        values: [
          { value: "api", count: 15 },
          { value: "", count: 5 },
        ],
        total: 20,
        sampled: false,
      });
    }),
  );
  const onFilter = vi.fn();
  const onKeyChange = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <TopValuesPanel
        signal="logs"
        range={{ range: "1h" }}
        filter={{ filters: [{ key: "host.id", op: "=", value: "h-1" }], groups: [[{ key: "a", op: "exists" }], [{ key: "b", op: "exists" }]], q: "timeout" }}
        context={{ transaction: "GET /cart", transactionService: "checkout" }}
        keyName="service.name"
        onKeyChange={onKeyChange}
        suggestions={["severity_text"]}
        canFilter
        onFilter={onFilter}
        onClose={() => {}}
        {...props}
      />
    </QueryClientProvider>,
  );
  return { requests, onFilter, onKeyChange };
}

describe("TopValuesPanel", () => {
  it("shows the top values of the current query with counts and shares", async () => {
    const { requests } = renderPanel();
    const items = await screen.findAllByTestId("top-value");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("api");
    expect(items[0]).toHaveTextContent("75%");
    expect(items[1]).toHaveTextContent("(empty)");
    expect(items[1]).toHaveTextContent("25%");
    const p = requests[0]!.searchParams;
    expect(p.get("key")).toBe("service.name");
    expect(p.get("limit")).toBe("10");
    expect(JSON.parse(p.get("filters")!)).toEqual([{ key: "host.id", op: "=", value: "h-1" }]);
    expect(JSON.parse(p.get("groups")!)).toHaveLength(2);
    expect(p.get("body_q")).toBe("timeout");
    expect(p.get("transaction")).toBe("GET /cart");
    expect(p.get("transaction_service")).toBe("checkout");
    expect(screen.getByText(/of 20 records/)).toBeInTheDocument();
  });

  it("filters in and out on click", async () => {
    const user = userEvent.setup();
    const { onFilter } = renderPanel();
    const first = (await screen.findAllByTestId("top-value"))[0]!;
    await user.click(within(first).getByRole("button", { name: "Filter for service.name = api" }));
    await user.click(within(first).getByRole("button", { name: "Exclude service.name = api" }));
    expect(onFilter.mock.calls).toEqual([
      ["service.name", "api", false, "string"],
      ["service.name", "api", true, "string"],
    ]);
  });

  it("offers suggested keys before a key is picked and disables filtering at the limit", async () => {
    const user = userEvent.setup();
    const { onKeyChange, requests } = renderPanel({ keyName: undefined });
    await user.click(screen.getByRole("button", { name: "severity_text" }));
    expect(onKeyChange).toHaveBeenCalledWith("severity_text");
    expect(requests).toHaveLength(0);
  });
});
