import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ssoErrorCode, ssoLogoutStatus } from "@/api/sso";
import { resetMockSso, setMockSsoConnection } from "@/mocks/sso";
import { SsoLogoutNotice } from "./SsoLogoutNotice";
import { SsoSignIn } from "./SsoSignIn";

describe("SsoSignIn", () => {
  beforeEach(() => resetMockSso());

  it("discovers the organization and continues at the identity provider", async () => {
    setMockSsoConnection({ enabled: true });
    const navigate = vi.fn();
    const user = userEvent.setup();
    render(<SsoSignIn onBack={vi.fn()} navigate={navigate} redirect="/apm" notice="Your organization requires single sign-on." />);
    expect(screen.getByRole("status")).toHaveTextContent("requires single sign-on");

    await user.type(screen.getByLabelText("Work e-mail"), "someone@elsewhere.example");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("not set up for this e-mail domain");
    expect(navigate).not.toHaveBeenCalled();

    await user.clear(screen.getByLabelText("Work e-mail"));
    await user.type(screen.getByLabelText("Work e-mail"), "Someone@openlog.local");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith("/apm"));
  });

  it("requires an e-mail address and goes back to the password form", async () => {
    const onBack = vi.fn();
    const user = userEvent.setup();
    render(<SsoSignIn onBack={onBack} navigate={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByLabelText("Work e-mail")).toHaveAttribute("aria-invalid", "true");
    await user.click(screen.getByRole("button", { name: "Sign in with password" }));
    expect(onBack).toHaveBeenCalled();
  });

  it("accepts only known sso_error codes", () => {
    expect(ssoErrorCode("domain_not_verified")).toBe("domain_not_verified");
    expect(ssoErrorCode("<script>alert(1)</script>")).toBeUndefined();
    expect(ssoErrorCode(42)).toBeUndefined();
  });

  it("explains the result of signing out everywhere (?sso_logout=)", () => {
    expect(ssoLogoutStatus("ok")).toBe("ok");
    expect(ssoLogoutStatus("partial")).toBe("partial");
    expect(ssoLogoutStatus("done")).toBeUndefined();

    const { rerender, container } = render(<SsoLogoutNotice status="ok" />);
    expect(screen.getByRole("status")).toHaveTextContent("You are signed out of openlog and your identity provider.");
    rerender(<SsoLogoutNotice status="partial" />);
    expect(screen.getByRole("status")).toHaveTextContent("the identity provider did not confirm the sign-out. Close the browser");
    rerender(<SsoLogoutNotice status={undefined} />);
    expect(container).toBeEmptyDOMElement();
  });
});
