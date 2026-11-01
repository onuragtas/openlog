// The integrations list is built in the browser out of one inventory read, so a heading and Next never reach the server.
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { IntegrationRow } from "@/lib/integrations";
import { IntegrationsTable } from "./integrations";

// uPlot needs matchMedia/canvas, and the route brings it along with the panels.
vi.mock("@/components/TimeSeriesChart", () => ({ TimeSeriesChart: () => null }));

const row = (i: number): IntegrationRow => ({
  hostId: `h${i}`,
  hostName: `host-${String(i).padStart(2, "0")}`,
  key: `redis/${i}`,
  discoveryId: "redis",
  instance: `:${6000 + i}`,
  name: `service-${String(99 - i).padStart(2, "0")}`,
  integration: { id: "redis", status: i === 59 ? "error" : "enabled" },
  panel: false,
});

const ROWS = Array.from({ length: 60 }, (_, i) => row(i));

function renderTable(ui: () => React.ReactElement) {
  const router = createRouter({ routeTree: createRootRoute({ component: ui }), history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(<RouterProvider router={router} />);
}

const hosts = () => within(screen.getByTestId("integrations-table")).getAllByTestId("integration-row").map((r) => within(r).getAllByRole("cell")[0]!.textContent);
const heading = (name: string) => within(screen.getByTestId("integrations-table")).getByRole("button", { name });

describe("IntegrationsTable", () => {
  it("sorts and pages the rows it was given", async () => {
    const user = userEvent.setup();
    renderTable(() => <IntegrationsTable rows={ROWS} resetKey="" />);
    await screen.findByTestId("integrations-table");
    expect(hosts()).toHaveLength(50);
    expect(hosts()[0]).toBe("host-00");

    await user.click(heading("Service"));
    await waitFor(() => expect(hosts()[0]).toBe("host-59"));
    expect(screen.getByTestId("integrations-table").querySelector("[aria-sort=ascending]")).toHaveTextContent("Service");

    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(hosts()).toHaveLength(10));
    expect(hosts()[9]).toBe("host-00");
  });

  it("goes back to the first page when a filter changes", async () => {
    const user = userEvent.setup();
    renderTable(() => {
      const [key, setKey] = useState("");
      return (
        <>
          <button type="button" onClick={() => setKey("q")}>filter</button>
          <IntegrationsTable rows={ROWS} resetKey={key} />
        </>
      );
    });
    await screen.findByTestId("integrations-table");
    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(hosts()).toHaveLength(10));
    await user.click(screen.getByRole("button", { name: "filter" }));
    await waitFor(() => expect(hosts()).toHaveLength(50));
  });
});
