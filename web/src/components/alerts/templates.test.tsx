import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockAlerts } from "@/mocks/alerts";
import { ThemeProvider } from "@/lib/theme";

// uPlot reads window.matchMedia when it loads; jsdom has none.
vi.hoisted(() => {
  if (typeof window !== "undefined" && !window.matchMedia) {
    Object.defineProperty(window, "matchMedia", {
      value: () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }),
    });
  }
});
import { MutesManager } from "./MutesManager";
import { TemplateGallery } from "./TemplateGallery";

function renderUi(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRootRoute({ component: () => ui });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <ThemeProvider>
      <QueryClientProvider client={client}>
        <RouterProvider router={router as never} />
      </QueryClientProvider>
    </ThemeProvider>,
  );
}

describe("recommended alert templates", () => {
  beforeEach(() => resetMockAlerts());

  it("sets up an integration template for an instance, previews and creates it in one click", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<TemplateGallery category="integration" integration="redis" target={{ hostId: "h-1", hostName: "cache-1", discoveryId: "redis", instance: "/usr/bin/redis-server" }} />);

    const cards = await screen.findAllByTestId("alert-template");
    expect(cards.map((c) => within(c).getByText(/Redis/, { selector: "p" }).textContent)).toEqual(["Redis memory near maxmemory", "Redis evicting keys"]);

    await user.click(within(cards[0]!).getByRole("button", { name: "Set up Redis memory near maxmemory" }));
    // Target parameters are fixed; the ratio is edited as percent.
    expect(within(cards[0]!).queryByLabelText(/Instance/)).not.toBeInTheDocument();
    const ratio = within(cards[0]!).getByLabelText("Share of maxmemory (%)");
    expect(ratio).toHaveValue("90");
    expect(await within(cards[0]!).findByTestId("template-reference")).toHaveTextContent("redis.maxmemory is 1.0 GiB now: threshold 921.6 MiB.");

    await user.clear(ratio);
    await user.type(ratio, "150");
    expect(within(cards[0]!).getByText("Must be between 1 and 100.")).toBeInTheDocument();
    await user.clear(ratio);
    await user.type(ratio, "80");

    const create = within(cards[0]!).getByRole("button", { name: "Create rule" });
    await vi.waitFor(() => expect(create).toBeEnabled(), { timeout: 3000 });
    await user.click(create);
    const done = await within(cards[0]!).findByTestId("template-created");
    expect(done).toHaveTextContent("Redis memory near maxmemory – /usr/bin/redis-server");
    expect(within(cards[0]!).getByRole("link", { name: "Customize in editor" })).toBeInTheDocument();
  });

  it("hides ratio templates without an instance and groups the catalog by category", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderUi(<TemplateGallery />);
    expect(await screen.findByRole("heading", { name: "Hosts" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Integration: redis" })).toBeInTheDocument();
    expect(screen.queryByText("Redis memory near maxmemory")).not.toBeInTheDocument();
    expect(screen.getByText("Redis evicting keys")).toBeInTheDocument();
  });
});

describe("recurring mute calendars", () => {
  beforeEach(() => resetMockAlerts());

  it("builds a monthly rule with exceptions and shows the next occurrences", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderUi(<MutesManager />);
    expect(await screen.findByTestId("calendar-row")).toHaveTextContent("TR public holidays");

    await user.click(await screen.findByRole("button", { name: "New mute" }));
    await user.click(screen.getByRole("checkbox", { name: "Repeat on selected days" }));
    await user.selectOptions(screen.getByLabelText("Repeats"), "monthly");
    const days = screen.getByLabelText("Days of the month");
    await user.clear(days);
    await user.type(days, "0");
    expect(screen.getByText("Enter days between 1 and 31 or -1 … -31.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create mute" })).toBeDisabled();
    await user.clear(days);
    await user.type(days, "1, 15");

    await user.click(screen.getByRole("radio", { name: "Weekday of the month" }));
    await user.selectOptions(screen.getByLabelText("Which"), "-1");
    expect(screen.getByLabelText("Day")).toHaveValue("mon");

    await user.type(screen.getByLabelText("Date"), "2030-01-01");
    await user.click(screen.getByRole("button", { name: "Add date" }));
    expect(within(screen.getByRole("list", { name: "Exception dates" })).getByText("2030-01-01")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: /TR public holidays/ }));

    const upcoming = screen.getByTestId("mute-upcoming");
    await vi.waitFor(() => expect(within(upcoming).getAllByRole("listitem").length).toBe(5), { timeout: 3000 });
  });
});
