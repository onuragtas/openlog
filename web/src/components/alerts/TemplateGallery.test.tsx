import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { setSupportSession } from "@/api/supportSession";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockAlerts } from "@/mocks/alerts";
import { resetMockOperator, setMockOrgSuspended } from "@/mocks/operator";
import { TemplateGallery } from "./TemplateGallery";

// uPlot needs matchMedia/canvas; the preview chart is covered elsewhere.
vi.mock("./PreviewChart", () => ({ PreviewChart: ({ title }: { title: string }) => <div data-testid="preview-chart">{title}</div> }));

const TARGET = { hostId: "h-1", hostName: "cache-1", discoveryId: "redis", instance: "/usr/bin/redis-server" };

function renderUi(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRootRoute({ component: () => ui });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  );
}

/** Opens the first Redis template and waits until its rule has been rendered (debounced parameters applied). */
async function openTemplate() {
  const user = userEvent.setup();
  renderUi(<TemplateGallery category="integration" integration="redis" target={TARGET} />);
  const card = (await screen.findAllByTestId("alert-template"))[0]!;
  await user.click(within(card).getByRole("button", { name: "Set up Redis memory near maxmemory" }));
  await within(card).findByTestId("template-reference");
  return card;
}

beforeEach(async () => {
  resetMockAlerts();
  resetMockOperator();
  setSupportSession(null);
  await login(MOCK_EMAIL, MOCK_PASSWORD);
});
afterEach(() => {
  resetMockOperator();
  setSupportSession(null);
});

describe("alert template gallery in a read-only organization", { timeout: 20_000 }, () => {
  it("active organization: create and customize are available", async () => {
    const card = await openTemplate();
    await waitFor(() => expect(within(card).getByRole("button", { name: "Create rule" })).toBeEnabled(), { timeout: 3000 });
    expect(await within(card).findByRole("link", { name: "Customize in editor" })).toBeInTheDocument();
  });

  it("suspended organization: create is disabled with the reason and Customize is hidden", async () => {
    setMockOrgSuspended("default", true);
    const card = await openTemplate();
    const create = () => within(card).getByRole("button", { name: "Create rule" });
    await waitFor(() => expect(create()).toBeDisabled());
    expect(create().closest("[data-testid=read-only-guard]")).toHaveAttribute("title", expect.stringContaining("suspended"));
    expect(create().closest("fieldset")).toHaveAccessibleDescription(/suspended/);
    expect(within(card).queryByRole("link", { name: "Customize in editor" })).not.toBeInTheDocument();
  });

  it("operator support view: create is disabled and Customize is hidden", async () => {
    setSupportSession({ id: "ss-1", org_id: "o", org_name: "Acme", expires_at: new Date(Date.now() + 3_600_000).toISOString() });
    const card = await openTemplate();
    const create = () => within(card).getByRole("button", { name: "Create rule" });
    await waitFor(() => expect(create()).toBeDisabled());
    expect(create().closest("[data-testid=read-only-guard]")).toHaveAttribute("title", expect.stringContaining("Support view"));
    expect(within(card).queryByRole("link", { name: "Customize in editor" })).not.toBeInTheDocument();
  });
});
