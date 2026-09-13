import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { LicenseKeysSettings } from "./LicenseKeysSettings";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("LicenseKeysSettings", () => {
  it("creates a key, shows it once and revokes it", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<LicenseKeysSettings />);

    expect(await screen.findByText("production hosts")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Key name"), "edge nodes");
    await user.click(screen.getByRole("button", { name: "Create key" }));

    const reveal = await screen.findByTestId("secret-reveal");
    const secret = (within(reveal).getByRole("textbox") as HTMLInputElement).value;
    expect(secret).toMatch(/^olk_[0-9a-f]{48}$/);
    await user.click(within(reveal).getByRole("button", { name: "Done" }));
    expect(screen.queryByTestId("secret-reveal")).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(secret)).not.toBeInTheDocument();

    const row = (await screen.findByText("edge nodes")).closest("tr")!;
    expect(within(row).getByText(secret.slice(0, 12))).toBeInTheDocument();
    expect(row).not.toHaveTextContent(secret);
    await user.click(within(row).getByRole("button", { name: "Revoke" }));
    await user.click(within(row).getByRole("button", { name: "Confirm revoke" }));
    expect(await within(row).findByText("Revoked")).toBeInTheDocument();
  });

  it("viewers cannot create or list keys", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // the mock user is a viewer there
    renderWithClient(<LicenseKeysSettings />);
    expect(await screen.findByRole("alert")).toHaveTextContent("does not allow");
    expect(screen.queryByRole("button", { name: "Create key" })).not.toBeInTheDocument();
  });
});
