import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD, setMockAuthConfig, setMockEmailVerified } from "@/mocks/account";
import { EmailVerificationBanner } from "./EmailVerificationBanner";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("EmailVerificationBanner", () => {
  it("asks unverified users to confirm and resends the e-mail", async () => {
    setMockAuthConfig({ email_enabled: true, email_verification_required: true });
    setMockEmailVerified(false);
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<EmailVerificationBanner />);
    expect(await screen.findByText(/Confirm your e-mail address \(admin@openlog.local\)/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Resend e-mail" }));
    expect(await screen.findByText("A new confirmation e-mail is on its way.")).toBeInTheDocument();
  });

  it("stays hidden for verified users", async () => {
    setMockAuthConfig({ email_enabled: true, email_verification_required: true });
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const { container } = renderWithClient(<EmailVerificationBanner />);
    await new Promise((r) => setTimeout(r, 50));
    expect(container).toBeEmptyDOMElement();
  });
});
