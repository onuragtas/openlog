import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { acceptInvitation, lookupInvitation } from "@/api/account";
import { ApiError } from "@/api/client";
import { MOCK_INVITE_TOKEN, MOCK_SSO_EXTERNAL_INVITE_TOKEN, MOCK_SSO_INVITE_TOKEN } from "@/mocks/account";
import { resetMockSso, setMockSsoConnection } from "@/mocks/sso";
import { InvitationSso } from "./InvitationSso";

const loginLink = <a href="/login">Go to sign in</a>;

describe("InvitationSso", () => {
  beforeEach(() => resetMockSso());

  it("the invitation lookup names the single sign-on of a claimed domain", async () => {
    expect((await lookupInvitation(MOCK_SSO_INVITE_TOKEN)).sso).toBeNull(); // no enabled connection yet
    setMockSsoConnection({ enabled: true, name: "Okta" });
    expect((await lookupInvitation(MOCK_INVITE_TOKEN)).sso).toBeNull();
    const info = await lookupInvitation(MOCK_SSO_INVITE_TOKEN);
    expect(info.sso).toEqual({ required: true, organization_name: "Default", connection_name: "Okta", protocol: "oidc", same_organization: true });
    // Invitations of other organizations need allow_external_invitations=false on the claiming connection.
    expect((await lookupInvitation(MOCK_SSO_EXTERNAL_INVITE_TOKEN)).sso).toBeNull();
    setMockSsoConnection({ enabled: true, name: "Okta", allow_external_invitations: false });
    expect((await lookupInvitation(MOCK_SSO_EXTERNAL_INVITE_TOKEN)).sso?.same_organization).toBe(false);
    // Password acceptance is refused.
    const err = await acceptInvitation(MOCK_SSO_INVITE_TOKEN, "long enough pw", "").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("failed_precondition");
  });

  it("offers single sign-on for an invitation of the claiming organization", async () => {
    setMockSsoConnection({ enabled: true });
    const navigate = vi.fn();
    const user = userEvent.setup();
    render(
      <InvitationSso
        organizationName="Default"
        email="new.hire@openlog.local"
        roleLabel="Member"
        sso={{ required: true, organization_name: "Default", connection_name: "Okta", protocol: "oidc", same_organization: true }}
        loginLink={loginLink}
        navigate={navigate}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Default uses single sign-on for new.hire@openlog.local.");
    expect(screen.getByText("Sign in with single sign-on to accept the invitation.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Continue with SSO" }));
    expect(screen.getByRole("heading", { name: "Sign in with single sign-on" })).toBeInTheDocument();
    expect(screen.getByLabelText("Work e-mail")).toHaveValue("new.hire@openlog.local");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith("/hosts"));
  });

  it("explains that another organization's invitation needs another address", () => {
    render(
      <InvitationSso
        organizationName="Staging"
        email="contractor@openlog.local"
        roleLabel="Member"
        sso={{ required: true, organization_name: "Default", connection_name: "Okta", protocol: "oidc", same_organization: false }}
        loginLink={loginLink}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Default uses single sign-on for contractor@openlog.local.");
    expect(screen.getByText(/ask the inviting organization to invite another address/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Continue with SSO" })).not.toBeInTheDocument();
  });
});
