import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { ApiKeysSettings } from "./ApiKeysSettings";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("ApiKeysSettings", () => {
  it("creates a read-only key by default", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<ApiKeysSettings />);

    expect(await screen.findByText("grafana")).toBeInTheDocument();
    expect(screen.getByLabelText("Role")).toHaveValue("viewer");
    await user.type(screen.getByLabelText("Key name"), "dashboards");
    await user.click(screen.getByRole("button", { name: "Create API key" }));

    const reveal = await screen.findByTestId("secret-reveal");
    expect((within(reveal).getByRole("textbox") as HTMLInputElement).value).toMatch(/^ola_[0-9a-f]{48}$/);
    expect(reveal).toHaveTextContent("can only read data");
    await user.click(within(reveal).getByRole("button", { name: "Done" }));

    const row = (await screen.findByText("dashboards")).closest("tr")!;
    expect(within(row).getByText("Viewer")).toBeInTheDocument();
  });

  it("lets an owner create a key that can change configuration", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD); // the mock user owns the default organization
    const user = userEvent.setup();
    renderWithClient(<ApiKeysSettings />);
    expect(await screen.findByText("grafana")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Key name"), "terraform ci");
    await user.selectOptions(screen.getByLabelText("Role"), "admin");
    await user.click(screen.getByRole("button", { name: "Create API key" }));

    const reveal = await screen.findByTestId("secret-reveal");
    expect(reveal).toHaveTextContent("change configuration");
    await user.click(within(reveal).getByRole("button", { name: "Done" }));

    const row = (await screen.findByText("terraform ci")).closest("tr")!;
    expect(within(row).getByText("Admin")).toBeInTheDocument();
  });

  it("marks a key that can write differently from a read-only one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<ApiKeysSettings />);

    const readOnly = within((await screen.findByText("grafana")).closest("tr")!).getByText("Viewer");
    const writing = within(screen.getByText("terraform").closest("tr")!).getByText("Admin");
    expect(writing.className).toContain("warning");
    expect(readOnly.className).not.toContain("warning");
  });

  it("viewers cannot create or list keys", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // the mock user is a viewer there
    renderWithClient(<ApiKeysSettings />);
    expect(await screen.findByRole("alert")).toHaveTextContent("does not allow");
    expect(screen.queryByRole("button", { name: "Create API key" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Role")).not.toBeInTheDocument();
  });
});
