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

  it("imports an existing key value without revealing it", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<LicenseKeysSettings />);
    expect(await screen.findByText("production hosts")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Key name"), "signoz senders");
    const toggle = screen.getByRole("switch", { name: "Use my own key value" });
    expect(toggle).toHaveAttribute("aria-checked", "false");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-checked", "true");

    const input = screen.getByLabelText("Key value") as HTMLInputElement;
    expect(input.type).toBe("password");
    await user.click(screen.getByRole("button", { name: "Show key value" }));
    expect(input.type).toBe("text");
    await user.click(screen.getByRole("button", { name: "Hide key value" }));
    expect(input.type).toBe("password");

    const create = screen.getByRole("button", { name: "Create key" });
    await user.type(input, "too short");
    expect(screen.getByRole("alert")).toHaveTextContent("printable ASCII");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(create).toBeDisabled();
    await user.clear(input);
    await user.type(input, "tooshort");
    expect(screen.getByRole("alert")).toHaveTextContent("16–256 characters (8 now)");
    expect(create).toBeDisabled();

    const value = "0123456789abcdef0123456789abcdef";
    await user.clear(input);
    await user.type(input, value);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await user.click(create);

    const done = await screen.findByTestId("license-key-imported");
    expect(done).toHaveTextContent("signoz senders");
    expect(screen.queryByTestId("secret-reveal")).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(value)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Key value")).not.toBeInTheDocument();

    const row = (await screen.findByText("signoz senders")).closest("tr")!;
    expect(within(row).getByText("custom")).toBeInTheDocument();
    expect(within(row).getByText("01234567")).toBeInTheDocument();
    expect(row).not.toHaveTextContent(value);

    // The same value again is rejected by the API (409).
    await user.type(screen.getByLabelText("Key name"), "again");
    await user.click(screen.getByRole("switch", { name: "Use my own key value" }));
    await user.type(screen.getByLabelText("Key value"), value);
    await user.click(screen.getByRole("button", { name: "Create key" }));
    expect(await screen.findByText(/already in use/)).toBeInTheDocument();
  });

  it("viewers cannot create or list keys", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // the mock user is a viewer there
    renderWithClient(<LicenseKeysSettings />);
    expect(await screen.findByRole("alert")).toHaveTextContent("does not allow");
    expect(screen.queryByRole("button", { name: "Create key" })).not.toBeInTheDocument();
  });
});
