import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";

// uPlot reads window.matchMedia when it loads; jsdom has none.
vi.hoisted(() => {
  if (typeof window !== "undefined" && !window.matchMedia) {
    Object.defineProperty(window, "matchMedia", {
      value: () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }),
    });
  }
});
import { takeRenderToken } from "@/api/dashboardRender";
import { server } from "@/mocks/server";
import { PrintDashboardView } from "./PrintDashboardView";

const TOKEN = "olrt_eyJvIjoiMSJ9." + "a".repeat(43);

const dashboard = {
  name: "Checkout",
  description: "",
  variables: [],
  time_range: { range: null, from: "2026-09-13T09:00:00Z", to: "2026-09-14T09:00:00Z" },
  expires_at: "2026-09-14T09:05:00Z",
  pages: [
    {
      id: "p1",
      name: "Overview",
      widgets: [
        { id: "w1", title: "Errors", visualization: "billboard", layout: { x: 0, y: 0, w: 6, h: 3 }, markdown: "", unit: "number", thresholds: [], options: {} },
        { id: "w2", title: "Notes", visualization: "markdown", layout: { x: 6, y: 0, w: 6, h: 3 }, markdown: "hi", unit: "number", thresholds: [], options: {} },
        { id: "w3", title: "Broken", visualization: "table", layout: { x: 0, y: 3, w: 6, h: 3 }, markdown: "", unit: "number", thresholds: [], options: {} },
      ],
    },
  ],
};

const single = {
  kind: "single",
  columns: [{ name: "count(*)", function: "count", type: "number" }],
  facets: [],
  rows: [{ facets: [], values: [42] }],
  series: [],
  buckets: [],
  metadata: { from: "2026-09-13T09:00:00Z", to: "2026-09-14T09:00:00Z", truncated: false, table: "", rows_read: 0, bytes_read: 0, warnings: [] },
};

function renderView() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <PrintDashboardView />
    </QueryClientProvider>,
  );
}

describe("PrintDashboardView", () => {
  afterEach(() => {
    delete document.documentElement.dataset.renderState;
    delete document.documentElement.dataset.renderMessage;
    window.history.replaceState(null, "", "/");
  });

  it("reads the token from the fragment, renders query widgets and reports ready", async () => {
    const auth: string[] = [];
    server.use(
      http.get("*/api/v1/render/dashboard", ({ request }) => {
        auth.push(request.headers.get("Authorization") ?? "");
        return HttpResponse.json(dashboard);
      }),
      http.get("*/api/v1/render/dashboard/widgets/w1/result", () => HttpResponse.json(single)),
      http.get("*/api/v1/render/dashboard/widgets/w3/result", () =>
        HttpResponse.json({ error: { code: "invalid_argument", message: "this widget's query cannot be run" } }, { status: 422 }),
      ),
    );
    window.history.replaceState(null, "", "/print/dashboard#token=" + TOKEN);
    const { container } = renderView();
    expect(window.location.hash).toBe("");
    await waitFor(() => expect(document.documentElement.dataset.renderState).toBe("ready"));
    expect(auth[0]).toBe("Bearer " + TOKEN);
    const blocks = container.querySelectorAll("[data-render-id]");
    expect([...blocks].map((b) => b.getAttribute("data-render-id"))).toEqual(["w1", "w3"]);
    expect(blocks[0]?.getAttribute("data-render-error")).toBeNull();
    expect(blocks[1]?.getAttribute("data-render-error")).toBe("1");
    expect(screen.getByTestId("print-dashboard")).toHaveTextContent("42");
  });

  it("reports an error without a valid token", async () => {
    window.history.replaceState(null, "", "/print/dashboard#token=olds_share");
    renderView();
    await waitFor(() => expect(document.documentElement.dataset.renderState).toBe("error"));
    expect(document.documentElement.dataset.renderMessage).toContain("token");
  });

  it("reports an error when the dashboard cannot be loaded", async () => {
    server.use(http.get("*/api/v1/render/dashboard", () => HttpResponse.json({ error: { code: "unauthenticated", message: "invalid or expired render token" } }, { status: 401 })));
    window.history.replaceState(null, "", "/print/dashboard#token=" + TOKEN);
    renderView();
    await waitFor(() => expect(document.documentElement.dataset.renderState).toBe("error"));
    expect(document.documentElement.dataset.renderMessage).toContain("expired");
  });

  it("only accepts render tokens", () => {
    const hist = { replaceState: () => {} } as unknown as History;
    expect(takeRenderToken({ hash: "#token=" + TOKEN, pathname: "/print/dashboard" } as Location, hist)).toBe(TOKEN);
    expect(takeRenderToken({ hash: "#token=olds_x", pathname: "/print/dashboard" } as Location, hist)).toBeNull();
    expect(takeRenderToken({ hash: "", pathname: "/print/dashboard" } as Location, hist)).toBeNull();
  });
});
