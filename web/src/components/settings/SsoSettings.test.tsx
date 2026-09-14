import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { beforeEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { addMockSsoConnection, resetMockSso, setMockSsoConnection } from "@/mocks/sso";
import { SsoSettings } from "./SsoSettings";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

const OIDC = { issuer: "https://idp.example.com", client_id: "openlog", scopes: [], require_email_verified: true, client_secret_set: true };

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

    // No connection yet: the wizard is shown directly (protocol → service provider values → identity provider).
    expect(screen.getByRole("radio", { name: /OpenID Connect/ })).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByLabelText("Redirect URI")).toHaveValue("http://localhost:3000/api/v1/sso/oidc/callback");
    expect(screen.getByLabelText("Post-logout redirect URI")).toHaveValue("http://localhost:3000/api/v1/sso/oidc/logout/callback");
    await user.click(screen.getByRole("button", { name: "Next" }));
    await user.type(screen.getByLabelText("Issuer URL"), "https://idp.example.com");
    await user.type(screen.getByLabelText("Client ID"), "openlog");
    await user.type(screen.getByLabelText("Client secret"), "s3cret");
    await user.click(screen.getByRole("button", { name: "Save connection" }));
    expect(await screen.findByText("Connection saved.")).toBeInTheDocument();
    expect(screen.getByText(/A secret is stored/)).toBeInTheDocument();
    expect(screen.queryByDisplayValue("s3cret")).not.toBeInTheDocument();
    // The saved connection is listed as the default connection.
    const list = await screen.findByRole("list", { name: "Single sign-on connections" });
    expect(within(list).getByText("Default")).toBeInTheDocument();

    // Server-side checks.
    await user.click(screen.getByRole("button", { name: "Check configuration" }));
    const checks = await screen.findByRole("list", { name: "Check configuration" });
    expect(within(checks).getByText("discovery")).toBeInTheDocument();

    // Enforcement is not possible before a test sign-in.
    expect(screen.getByRole("button", { name: "Enforce SSO" })).toBeDisabled();

    // Role mappings (organization-wide).
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
  }, 30_000);

  it("enforces SSO once the prerequisites are met", async () => {
    setMockSsoConnection({ enabled: true, tested: true, config_version: 2, oidc: OIDC });
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
    expect(screen.getByRole("status")).toHaveTextContent("Single sign-on is enforced.");
    const list = screen.getByRole("list", { name: "Single sign-on connections" });
    expect(within(list).getByText("Enforced")).toBeInTheDocument();

    // A connection under enforcement can be neither disabled nor deleted.
    await user.click(within(list).getByRole("button", { name: "Disable" }));
    expect(await within(list).findByText(/before disabling the connection/)).toBeInTheDocument();
    await user.click(within(list).getByRole("button", { name: "Delete connection" }));
    await user.click(within(list).getByRole("button", { name: "Confirm delete" }));
    expect(await within(list).findByText(/before deleting the connection/)).toBeInTheDocument();
  });

  it("manages several connections: domain routing, enable toggle, health refresh and per-connection mappings", async () => {
    setMockSsoConnection({ name: "Okta", enabled: true, tested: true, config_version: 1, oidc: OIDC });
    const second = addMockSsoConnection({
      name: "Contractors", protocol: "saml", enabled: true, config_version: 1,
      saml: {
        idp_metadata_url: "https://idp.example.com/metadata", idp_entity_id: "https://idp.example.com/metadata", idp_sso_url: "https://idp.example.com/sso",
        idp_slo_url: null, idp_certificates: ["AB"], idp_cert_not_after: null, allow_idp_initiated: false, relay_state_allowlist: [], sign_authn_requests: false,
      },
    });
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<SsoSettings />);

    const list = await screen.findByRole("list", { name: "Single sign-on connections" });
    const row = within(list).getByTestId(`sso-connection-${second.id}`);
    expect(within(row).getByText("SAML 2.0")).toBeInTheDocument();
    expect(within(row).getByText("Not checked yet")).toBeInTheDocument();
    expect(within(row).queryByText("Default")).not.toBeInTheDocument();

    // Refresh the IdP documents now.
    await user.click(within(row).getByRole("button", { name: "Refresh now" }));
    expect(await within(row).findByText("Healthy")).toBeInTheDocument();

    // Disable and enable again.
    await user.click(within(row).getByRole("button", { name: "Disable" }));
    expect(await within(row).findByText("Disabled")).toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: "Enable" }));
    expect(await within(row).findByText("Enabled")).toBeInTheDocument();

    // Route the verified domain to the second connection.
    const domain = screen.getByTestId("sso-domain-openlog.local");
    const select = within(domain).getByLabelText("Connection");
    expect(select).toHaveValue("");
    await user.selectOptions(select, second.id);
    await waitFor(() => expect(within(screen.getByTestId("sso-domain-openlog.local")).getByLabelText("Connection")).toHaveValue(second.id));

    // Per-connection role mappings.
    await user.selectOptions(screen.getByLabelText("Applies to"), second.id);
    expect(screen.getByText("A connection without its own mappings uses the organization-wide mappings.")).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Add mapping" }));
    await user.type(screen.getByLabelText("Group 1"), "contractors");
    await user.click(screen.getByRole("button", { name: "Save mappings" }));
    expect(await screen.findByText("Mappings saved.")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Applies to"), "");
    expect(await screen.findByText(/No mappings/)).toBeInTheDocument();

    // Edit opens the wizard of that connection with the SAML values.
    await user.click(within(row).getByRole("button", { name: "Edit Contractors" }));
    expect(await screen.findByRole("heading", { name: "Edit connection: Contractors" })).toBeInTheDocument();
    expect(screen.getByText(/no single logout service/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "2. Service provider" }));
    expect(await screen.findByLabelText("Single logout URL")).toHaveValue(`http://localhost:3000/api/v1/sso/saml/${second.id}/slo`);
    await user.click(screen.getByRole("button", { name: "4. Users and roles" }));
    await user.type(screen.getByLabelText("Logout redirect paths"), "/goodbye");
    await user.click(screen.getByLabelText("Allow invitations to other organizations with a password"));
    await user.click(screen.getByRole("button", { name: "Save connection" }));
    expect(await screen.findByText("Connection saved.")).toBeInTheDocument();

    // Enforcement is chosen per connection (the opened one) and "Contractors" now has the verified domain.
    const enforcement = screen.getByRole("heading", { name: "Enforce single sign-on" }).closest("section")!;
    expect(within(enforcement).getByLabelText("Connection")).toHaveValue(second.id);
    const domainRequirement = () =>
      within(within(enforcement).getByRole("list", { name: "Before enforcing" })).getByText("At least one verified domain signs in through this connection").closest("li");
    expect(domainRequirement()).toHaveAttribute("data-ok", "true");
    await user.selectOptions(within(enforcement).getByLabelText("Connection"), "0f7b3c1e-5a2d-4c1b-9e8f-0000000055c0");
    expect(domainRequirement()).toHaveAttribute("data-ok", "false");
  }, 30_000);

  it("is only available to administrators", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // viewer there
    renderWithClient(<SsoSettings />);
    expect(await screen.findByText("Only administrators can manage single sign-on.")).toBeInTheDocument();
  });
});
