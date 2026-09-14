import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { MOCK_EMAIL, setMockAuthConfig } from "@/mocks/account";
import { resetMockSso, setMockSsoConnection } from "@/mocks/sso";
import { SignupForm } from "./SignupForm";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

const loginLink = <a href="/login">Sign in</a>;

async function fill(user: ReturnType<typeof userEvent.setup>, email: string, password: string, confirm = password) {
  await user.type(screen.getByLabelText("Organization name"), "Acme");
  await user.type(screen.getByLabelText("Email"), email);
  await user.type(screen.getByLabelText("Password"), password);
  await user.type(screen.getByLabelText("Repeat password"), confirm);
}

describe("SignupForm", () => {
  it("explains that sign-up is disabled", async () => {
    renderWithClient(<SignupForm onSignedUp={vi.fn()} loginLink={loginLink} />);
    expect(await screen.findByText(/Sign-up is disabled on this server/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create account" })).not.toBeInTheDocument();
  });

  it("validates, reports conflicts and signs up", async () => {
    setMockAuthConfig({ signup_enabled: true, email_verification_required: true });
    const onSignedUp = vi.fn();
    const user = userEvent.setup();
    renderWithClient(<SignupForm onSignedUp={onSignedUp} loginLink={loginLink} />);
    expect(await screen.findByText("We will e-mail you a link to confirm your address.")).toBeInTheDocument();

    await fill(user, "new@example.com", "long enough pw", "different pw!!");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    expect(screen.getByRole("alert")).toHaveTextContent("The passwords do not match.");

    await user.clear(screen.getByLabelText("Repeat password"));
    await user.type(screen.getByLabelText("Repeat password"), "long enough pw");
    await user.clear(screen.getByLabelText("Email"));
    await user.type(screen.getByLabelText("Email"), MOCK_EMAIL);
    await user.click(screen.getByRole("button", { name: "Create account" }));
    expect(await screen.findByText(/already exists/)).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Email"));
    await user.type(screen.getByLabelText("Email"), "new@example.com");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    await vi.waitFor(() => expect(onSignedUp).toHaveBeenCalledTimes(1));
    const me = onSignedUp.mock.calls[0]![0];
    expect(me.user.email).toBe("new@example.com");
    expect(me.user.email_verified).toBe(false);
    expect(me.role).toBe("owner");
  });

  it("sends addresses of a domain claimed by single sign-on to SSO", async () => {
    resetMockSso();
    setMockSsoConnection({ enabled: true });
    setMockAuthConfig({ signup_enabled: true });
    const onSignedUp = vi.fn();
    const user = userEvent.setup();
    renderWithClient(<SignupForm onSignedUp={onSignedUp} loginLink={loginLink} navigate={vi.fn()} />);
    await screen.findByRole("button", { name: "Create account" });
    await fill(user, "someone@openlog.local", "long enough pw");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    expect(await screen.findByText("Default signs in with single sign-on for this e-mail domain.")).toBeInTheDocument();
    expect(onSignedUp).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Continue with SSO" }));
    expect(screen.getByRole("heading", { name: "Sign in with single sign-on" })).toBeInTheDocument();
    expect(screen.getByLabelText("Work e-mail")).toHaveValue("someone@openlog.local");
    resetMockSso();
  });

  it("requires the CAPTCHA when configured", async () => {
    setMockAuthConfig({ signup_enabled: true, captcha: { provider: "turnstile", site_key: "test-site" } });
    const user = userEvent.setup();
    renderWithClient(<SignupForm onSignedUp={vi.fn()} loginLink={loginLink} />);
    expect(await screen.findByTestId("captcha")).toBeInTheDocument();
    await fill(user, "new@example.com", "long enough pw");
    await user.click(screen.getByRole("button", { name: "Create account" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Complete the CAPTCHA first.");
  });
});
