import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { resetMockApm } from "@/mocks/apm";
import { DEFAULT_INBOX_FILTERS, ErrorInbox, type ErrorInboxFilterValues } from "./ErrorInbox";

const scope = { service: "frontend", namespace: "shop", environment: "prod" };
const range = { range: "1h" };

function Harness() {
  const [filters, setFilters] = useState<ErrorInboxFilterValues>(DEFAULT_INBOX_FILTERS);
  return <ErrorInbox range={range} scope={scope} filters={filters} onFilters={(p) => setFilters((f) => ({ ...f, ...p }))} onOpen={() => {}} />;
}

function renderInbox() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Harness />
    </QueryClientProvider>,
  );
}

afterEach(() => resetMockApm());

describe("ErrorInbox", () => {
  it("resolves selected groups in bulk and moves them to the Resolved tab", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderInbox();
    const inbox = await screen.findByTestId("error-inbox");
    expect(await within(inbox).findAllByRole("button", { name: /^Show error group/ })).toHaveLength(2);
    // The seeded UpstreamError group regressed after the 1.4.2 deployment.
    expect(within(inbox).getByText("Regressed")).toBeInTheDocument();
    const tabs = within(inbox).getByTestId("error-status-tabs");
    expect(within(tabs).getByRole("button", { name: /^Unresolved/ })).toHaveTextContent("2");

    await user.click(await within(inbox).findByLabelText("Select all listed error groups"));
    const bulk = within(inbox).getByTestId("error-bulk-actions");
    expect(bulk).toHaveTextContent("2 selected");
    await user.click(within(bulk).getByRole("button", { name: /^Resolve$/ }));

    expect(await within(inbox).findByText("No error groups match these filters in this range.")).toBeInTheDocument();
    await waitFor(() => expect(within(tabs).getByRole("button", { name: /^Resolved/ })).toHaveTextContent("2"));
    await user.click(within(tabs).getByRole("button", { name: /^Resolved/ }));
    expect(await within(inbox).findAllByRole("button", { name: /^Show error group/ })).toHaveLength(2);
  });

  it("validates the version of Resolve in version", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderInbox();
    const inbox = await screen.findByTestId("error-inbox");
    await user.click(await within(inbox).findByLabelText("Select error group TypeError"));
    const bulk = within(inbox).getByTestId("error-bulk-actions");
    await user.click(within(bulk).getByRole("button", { name: "Resolve in version…" }));
    await user.click(within(bulk).getByRole("button", { name: "Resolve in this version" }));
    expect(within(bulk).getByRole("alert")).toHaveTextContent("Enter a version.");
    await user.type(within(bulk).getByLabelText("Version"), "1.4.3");
    await user.click(within(bulk).getByRole("button", { name: "Resolve in this version" }));
    await waitFor(() => expect(within(inbox).getAllByRole("button", { name: /^Show error group/ })).toHaveLength(1));
  });

  it("hides selection and bulk actions from viewers", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // the mock user is a viewer there
    renderInbox();
    const inbox = await screen.findByTestId("error-inbox");
    expect(await within(inbox).findAllByRole("button", { name: /^Show error group/ })).toHaveLength(2);
    expect(within(inbox).queryByLabelText("Select all listed error groups")).not.toBeInTheDocument();
  });
});
