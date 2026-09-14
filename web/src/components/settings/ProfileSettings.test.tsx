import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import i18n, { LANGUAGE_STORAGE_KEY } from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { UserLanguageSync } from "@/components/UserLanguageSync";
import { OrganizationSettings } from "./OrganizationSettings";
import { ProfileSettings } from "./ProfileSettings";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("language preferences", () => {
  afterEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("saves the user's language, switches the UI and returns to the browser language with auto", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("en");
    renderWithClient(<ProfileSettings />);
    const select = await screen.findByLabelText("Language");
    expect(select).toHaveValue("auto");

    await userEvent.selectOptions(select, "tr");
    await waitFor(() => expect(i18n.resolvedLanguage).toBe("tr"));
    expect(await screen.findByRole("status")).toHaveTextContent("Dil tercihi kaydedildi.");
    expect(screen.getByLabelText("Dil")).toHaveValue("tr");

    localStorage.setItem(LANGUAGE_STORAGE_KEY, "tr");
    await userEvent.selectOptions(screen.getByLabelText("Dil"), "auto");
    await waitFor(() => expect(localStorage.getItem(LANGUAGE_STORAGE_KEY)).not.toBe("tr"));
  });

  it("applies a stored preference over the browser language", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("en");
    const { setMyLanguage } = await import("@/api/account");
    await setMyLanguage("tr");
    renderWithClient(<UserLanguageSync />);
    await waitFor(() => expect(i18n.resolvedLanguage).toBe("tr"));
  });

  it("lets admins set the organization's default e-mail language", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("en");
    renderWithClient(<OrganizationSettings />);
    const select = await screen.findByLabelText("E-mail language");
    expect(select).toHaveValue("");
    await userEvent.selectOptions(select, "tr");
    expect(await screen.findByText("Default e-mail language saved.")).toBeInTheDocument();
    expect(screen.getByLabelText("E-mail language")).toHaveValue("tr");
  });
});
