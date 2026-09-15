import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Link, retainSearchParams, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { AppShell } from "@/components/AppShell";
import { parseRefreshInterval, REFRESH_STORAGE } from "@/lib/auto-refresh";
import { ThemeProvider } from "@/lib/theme";
import { validateRangeSearch } from "@/lib/time";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";

const ABSOLUTE_HINT = "Auto-refresh works with relative time ranges (for example the last hour); a custom range does not move. Refresh now still reloads the data.";

beforeAll(() => {
  // Radix popper measures its anchor; jsdom has no ResizeObserver.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
});

let probeCalls = 0;
function LogsProbe() {
  useQuery({ queryKey: ["probe"], queryFn: async () => ++probeCalls });
  return (
    <Link to={"/metrics" as never} search={((prev: Record<string, unknown>) => ({ range: prev.range })) as never}>
      go to metrics
    </Link>
  );
}

function renderShell(url: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // Same root search handling as router.tsx.
  const root = createRootRoute({
    validateSearch: (s: Record<string, unknown>) => ({ ...validateRangeSearch(s), refresh: parseRefreshInterval(s.refresh) }),
    search: { middlewares: [retainSearchParams(["refresh"])] },
    component: AppShell,
  });
  const logs = createRoute({ getParentRoute: () => root, path: "/logs", component: LogsProbe });
  const metrics = createRoute({ getParentRoute: () => root, path: "/metrics", component: () => <p>metrics page</p> });
  const router = createRouter({ routeTree: root.addChildren([logs, metrics]), history: createMemoryHistory({ initialEntries: [url] }) });
  render(
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ThemeProvider>,
  );
  return router;
}

const search = (router: ReturnType<typeof renderShell>) => router.state.location.search as Record<string, unknown>;

describe("AppShell auto-refresh control", () => {
  it("shows the interval from the URL, refreshes on demand and keeps the interval across navigation", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    probeCalls = 0;
    const user = userEvent.setup();
    const router = renderShell("/logs?range=15m&refresh=30s");

    // Phone icon menu and desktop segmented control (breakpoint classes are not applied in jsdom).
    expect(await screen.findAllByRole("button", { name: "Auto-refresh every 30s" })).toHaveLength(2);
    await waitFor(() => expect(probeCalls).toBe(1));
    await user.click(screen.getByRole("button", { name: "Refresh now" }));
    await waitFor(() => expect(probeCalls).toBe(2));

    await user.click(screen.getByRole("link", { name: "go to metrics" }));
    expect(await screen.findByText("metrics page")).toBeInTheDocument();
    expect(search(router)).toMatchObject({ range: "15m", refresh: "30s" });
  });

  it("defaults to the remembered interval and stores a new choice", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    localStorage.setItem(REFRESH_STORAGE, "5m");
    const user = userEvent.setup();
    const router = renderShell("/logs?range=1h");

    const trigger = (await screen.findAllByRole("button", { name: "Auto-refresh every 5m" }))[1]!;
    trigger.focus();
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("menuitemradio", { name: "10s" }));
    await waitFor(() => expect(search(router).refresh).toBe("10s"));
    expect(localStorage.getItem(REFRESH_STORAGE)).toBe("10s");

    const again = screen.getAllByRole("button", { name: "Auto-refresh every 10s" })[1]!;
    again.focus();
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("menuitemradio", { name: "Off" }));
    await waitFor(() => expect(search(router).refresh).toBeUndefined());
    expect(localStorage.getItem(REFRESH_STORAGE)).toBe("off");
    expect(screen.getAllByRole("button", { name: "Auto-refresh: off" })).toHaveLength(2);
  });

  it("disables auto-refresh for an absolute range and explains why", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderShell("/logs?from=1700000000000&to=1700003600000&refresh=30s");

    const triggers = await screen.findAllByRole("button", { name: "Auto-refresh" });
    expect(triggers).toHaveLength(2);
    for (const t of triggers) expect(t).toHaveAccessibleDescription(ABSOLUTE_HINT);
    expect(screen.getByRole("button", { name: "Refresh now" })).toBeEnabled();

    await user.hover(triggers[1]!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(ABSOLUTE_HINT);

    triggers[1]!.focus();
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("menuitemradio", { name: "30s" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("menuitemradio", { name: "Off" })).not.toHaveAttribute("aria-disabled");
  });
});
