import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { resetMockSso, setMockSsoConnection } from "@/mocks/sso";
import { SsoSettings } from "./SsoSettings";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("SsoSettings", () => {
  beforeEach(() => resetMockSso());

  it("claims a domain, sets up OIDC, maps roles and creates a SCIM token", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<SsoSettings />);

    // Domains: the seeded domain is verified; a new one is verified through the (mock) DNS check.
    expect(await screen.findByText("openlog.local")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Domain"), "acme.example.com");
    await user.click(screen.getByRole("button", { name: "Add domain" }));
    const row = await screen.findByTestId("sso-domain-acme.example.com");
    expect(within(row).getByText("Not verified")).toBeInTheDocument();
    expect(within(row).getByLabelText("Record name")).toHaveValue("_openlog-verification.acme.example.com");
    await user.click(within(row).getByRole("button", { name: "Check DNS" }));
    expect(await within(row).findByText("Verified")).toBeInTheDocument();

    // Wizard: protocol → service provider values → identity provider.
    expect(screen.getByRole("radio", { name: /OpenID Connect/ })).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByLabelText("Redirect URI")).toHaveValue("http://localhost:3000/api/v1/sso/oidc/callback");
    await user.click(screen.getByRole("button", { name: "Next" }));
    await user.type(screen.getByLabelText("Issuer URL"), "https://idp.example.com");
    await user.type(screen.getByLabelText("Client ID"), "openlog");
    await user.type(screen.getByLabelText("Client secret"), "s3cret");
    await user.click(screen.getByRole("button", { name: "Save connection" }));
    expect(await screen.findByText("Connection saved.")).toBeInTheDocument();
    expect(screen.getByText(/A secret is stored/)).toBeInTheDocument();
    expect(screen.queryByDisplayValue("s3cret")).not.toBeInTheDocument();

    // Server-side checks.
    await user.click(screen.getByRole("button", { name: "Check configuration" }));
    const checks = await screen.findByRole("list", { name: "Check configuration" });
    expect(within(checks).getByText("discovery")).toBeInTheDocument();

    // Enforcement is not possible before a test sign-in.
    expect(screen.getByRole("button", { name: "Enforce SSO" })).toBeDisabled();

    // Role mappings.
    await user.click(screen.getByRole("button", { name: "Add mapping" }));
    await user.type(screen.getByLabelText("Group 1"), "openlog-admins");
    await user.selectOptions(screen.getByLabelText("Role 1"), "admin");
    await user.click(screen.getByRole("button", { name: "Save mappings" }));
    expect(await screen.findByText("Mappings saved.")).toBeInTheDocument();

    // SCIM token shown once.
    await user.type(screen.getByLabelText("Token name"), "okta");
    await user.click(screen.getByRole("button", { name: "Create SCIM token" }));
    const reveal = await screen.findByTestId("secret-reveal");
    expect((within(reveal).getByRole("textbox") as HTMLInputElement).value).toMatch(/^ols_[0-9a-f]{48}$/);
  });

  it("enforces SSO once the prerequisites are met", async () => {
    setMockSsoConnection({ enabled: true, tested: true, config_version: 2,
      oidc: { issuer: "https://idp.example.com", client_id: "openlog", scopes: [], require_email_verified: true, client_secret_set: true } });
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<SsoSettings />);

    const enforce = await screen.findByRole("button", { name: "Enforce SSO" });
    expect(enforce).toBeDisabled(); // no break-glass owner yet
    await user.click(await screen.findByRole("checkbox", { name: new RegExp(MOCK_EMAIL) }));
    expect(enforce).toBeEnabled();
    await user.click(enforce);
    expect(screen.getByRole("alert")).toHaveTextContent("lose access immediately");
    await user.click(screen.getByRole("button", { name: "Enforce now" }));
    expect(await screen.findByRole("button", { name: "Stop enforcing" })).toBeInTheDocument();
    // Shown in the enforcement status and as a badge on the connection.
    expect(screen.getAllByText("Single sign-on is enforced.")).toHaveLength(2);

    // A connection under enforcement cannot be deleted.
    await user.click(screen.getByRole("button", { name: "Delete connection" }));
    await user.click(screen.getByRole("button", { name: "Confirm delete" }));
    expect(await screen.findByText(/turn off single sign-on enforcement/)).toBeInTheDocument();
  });

  it("is only available to administrators", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // viewer there
    renderWithClient(<SsoSettings />);
    expect(await screen.findByText("Only administrators can manage single sign-on.")).toBeInTheDocument();
  });
});
