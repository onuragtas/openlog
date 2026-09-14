import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { login, meQuery } from "@/api/account";
import { continueSsoLogout, ssoSessionQuery } from "@/api/sso";
import { MOCK_EMAIL, MOCK_PASSWORD, mockAuth } from "@/mocks/account";
import { resetMockSso, setMockSsoConnection, setMockSsoSession } from "@/mocks/sso";
import { SsoSignOutButton } from "./SsoSignOutButton";

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const goToLogin = vi.fn();
  const assign = vi.fn();
  const onBeforeSignOut = vi.fn();
  render(
    <QueryClientProvider client={client}>
      <SsoSignOutButton goToLogin={goToLogin} assign={assign} onBeforeSignOut={onBeforeSignOut} />
    </QueryClientProvider>,
  );
  return { client, goToLogin, assign, onBeforeSignOut };
}

describe("SsoSignOutButton", () => {
  beforeEach(() => {
    resetMockSso();
    setMockSsoConnection({ enabled: true, name: "Okta" });
  });
  afterEach(() => setMockSsoSession(false));

  it("signs out of openlog and the identity provider", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setMockSsoSession(true);
    const user = userEvent.setup();
    const { client, goToLogin, assign, onBeforeSignOut } = setup();

    await user.click(await screen.findByRole("button", { name: "Sign out everywhere (IdP)" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/login?sso_logout=ok"));
    expect(onBeforeSignOut).toHaveBeenCalled();
    expect(goToLogin).not.toHaveBeenCalled();
    expect(mockAuth.get().signedIn).toBe(false);
    expect(client.getQueryData(meQuery().queryKey)).toBeUndefined();
  });

  it("is hidden for password sessions", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const { client } = setup();
    await waitFor(() => expect(client.getQueryState(ssoSessionQuery().queryKey)?.status).toBe("success"));
    expect(screen.queryByRole("button", { name: "Sign out everywhere (IdP)" })).not.toBeInTheDocument();
  });

  it("continues a SAML HTTP-POST logout with an auto-submitted form", () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(() => undefined);
    const assign = vi.fn();
    const done = continueSsoLogout(
      { protocol: "saml", redirect_url: null, post: { url: "https://idp.example.com/slo", fields: { SAMLRequest: "PHNhbWw+", RelayState: "abc" } }, revoked_sessions: 1 },
      assign,
    );
    expect(done).toBe(true);
    expect(assign).not.toHaveBeenCalled();
    const form = submit.mock.contexts[0] as HTMLFormElement;
    expect(form.method).toBe("post");
    expect(form.action).toBe("https://idp.example.com/slo");
    expect(new FormData(form).get("SAMLRequest")).toBe("PHNhbWw+");
    expect(new FormData(form).get("RelayState")).toBe("abc");
    form.remove();
    submit.mockRestore();
    expect(continueSsoLogout({ protocol: null, redirect_url: null, post: null, revoked_sessions: 1 }, assign)).toBe(false);
  });
});
