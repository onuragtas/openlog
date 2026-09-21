import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockBrowserKeys } from "@/mocks/browserKeys";
import { BrowserKeysSettings } from "./BrowserKeysSettings";

// The browser keys screen had no test, while every sibling credential screen has one. It is also the screen
// that now branches: a key is scoped by origins or by application ids, never both (rum.md §3.6), and the
// form has to re-point its validation at the other list when the kind changes. A form that kept validating
// origins would let an operator submit a mobile key with no allowlist at all and meet the refusal at the
// server instead.

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("BrowserKeysSettings", () => {
  beforeEach(() => resetMockBrowserKeys());

  it("shows a browser key scoped by its origins", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<BrowserKeysSettings />);

    const row = (await screen.findByText("Shop web")).closest("tr")!;
    expect(within(row).getByText(/shop\.example\.com/)).toBeInTheDocument();
    expect(screen.getByLabelText("Key type")).toHaveValue("browser");
    // A browser key asks for origins; application ids belong to the other kind and must not be offered.
    expect(screen.getByLabelText("Origins")).toBeInTheDocument();
    expect(screen.queryByLabelText("Applications")).not.toBeInTheDocument();
  });

  it("creates a mobile key scoped by application ids", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<BrowserKeysSettings />);
    expect(await screen.findByText("Shop web")).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Key type"), "mobile");
    // Switching the kind swaps the allowlist field, because a mobile application has no origin.
    expect(screen.queryByLabelText("Origins")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("Name"), "Shop android");
    await user.type(screen.getByLabelText("Application"), "shop-android");
    await user.type(screen.getByLabelText("Applications"), "com.example.shop");
    await user.click(screen.getByRole("button", { name: "Create key" }));

    const reveal = await screen.findByTestId("secret-reveal");
    await user.click(within(reveal).getByRole("button", { name: "Done" }));

    const row = (await screen.findByText("Shop android")).closest("tr")!;
    expect(within(row).getByText("com.example.shop")).toBeInTheDocument();
  });

  it("validates the allowlist the kind actually uses", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<BrowserKeysSettings />);
    expect(await screen.findByText("Shop web")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Name"), "Shop android");
    await user.type(screen.getByLabelText("Application"), "shop-android");
    await user.type(screen.getByLabelText("Origins"), "https://shop.example.com");
    const create = screen.getByRole("button", { name: "Create key" });
    expect(create).toBeEnabled();

    // The origins just typed do not scope a mobile key, so the form is incomplete again — this is the
    // assertion that fails if validation keeps reading the field the kind no longer uses.
    await user.selectOptions(screen.getByLabelText("Key type"), "mobile");
    expect(create).toBeDisabled();

    await user.type(screen.getByLabelText("Applications"), "com.example.shop");
    expect(create).toBeEnabled();
  });
});
