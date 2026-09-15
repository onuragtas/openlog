import { MutationObserver, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import { ApiError } from "@/api/client";
import { setSupportSession } from "@/api/supportSession";
import { ReadOnlyNotice, WriteGuard } from "@/components/ReadOnly";
import { LicenseKeysSettings } from "@/components/settings/LicenseKeysSettings";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockOperator, setMockOrgSuspended } from "@/mocks/operator";
import { createMutationCache, isOrgSuspendedError, usePermissions } from "./org-writable";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

function Probe() {
  const p = usePermissions();
  return (
    <div>
      <span data-testid="writable">{String(p.writable)}</span>
      <span data-testid="manage-keys">{String(p.can("license_keys.manage"))}</span>
      <span data-testid="list-keys">{String(p.can("license_keys.list"))}</span>
      <span data-testid="member">{String(p.canWriteAs("member"))}</span>
      <ReadOnlyNotice />
      <WriteGuard>
        <button type="button">Leave</button>
      </WriteGuard>
    </div>
  );
}

beforeEach(async () => {
  resetMockOperator();
  setSupportSession(null);
  await login(MOCK_EMAIL, MOCK_PASSWORD);
});
afterEach(() => {
  resetMockOperator();
  setSupportSession(null);
});

describe("read-only organization UI", () => {
  it("allows changes in an active organization", async () => {
    renderWithClient(<Probe />);
    await waitFor(() => expect(screen.getByTestId("manage-keys")).toHaveTextContent("true"));
    expect(screen.getByTestId("writable")).toHaveTextContent("true");
    expect(screen.getByRole("button", { name: "Leave" })).toBeEnabled();
    expect(screen.queryByTestId("read-only-notice")).not.toBeInTheDocument();
  });

  it("turns a suspended organization read-only: changes denied, reads allowed, disabled controls explained", async () => {
    setMockOrgSuspended("default", true);
    renderWithClient(<Probe />);
    expect(await screen.findByTestId("read-only-notice")).toHaveTextContent("This organization is suspended: changes are disabled");
    expect(screen.getByTestId("writable")).toHaveTextContent("false");
    expect(screen.getByTestId("manage-keys")).toHaveTextContent("false");
    expect(screen.getByTestId("list-keys")).toHaveTextContent("true");
    expect(screen.getByTestId("member")).toHaveTextContent("false");
    const leave = screen.getByRole("button", { name: "Leave" });
    expect(leave).toBeDisabled();
    expect(screen.getByTestId("read-only-guard")).toHaveAttribute("title", expect.stringContaining("suspended"));
    expect(leave.closest("fieldset")).toHaveAccessibleDescription(/suspended/);
  });

  it("treats an operator support view as read-only", async () => {
    setSupportSession({ id: "ss-1", org_id: "o", org_name: "Acme", expires_at: new Date(Date.now() + 3_600_000).toISOString() });
    renderWithClient(<Probe />);
    expect(await screen.findByTestId("read-only-notice")).toHaveTextContent("Support view");
    expect(screen.getByRole("button", { name: "Leave" })).toBeDisabled();
  });

  it("hides key management in a suspended organization but keeps the list", async () => {
    setMockOrgSuspended("default", true);
    renderWithClient(<LicenseKeysSettings />);
    expect(await screen.findByText("production hosts")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByLabelText("Key name")).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
  });

  it("refetches the SaaS state when a mutation is rejected with org_suspended", async () => {
    const client: QueryClient = new QueryClient({ mutationCache: createMutationCache(() => client) });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const suspended = new ApiError(403, "org_suspended", "this organization is suspended");
    expect(isOrgSuspendedError(suspended)).toBe(true);
    expect(isOrgSuspendedError(new ApiError(403, "permission_denied", "no"))).toBe(false);
    await new MutationObserver(client, { mutationFn: () => Promise.reject(new ApiError(403, "permission_denied", "no")) }).mutate().catch(() => undefined);
    expect(invalidate).not.toHaveBeenCalled();
    await new MutationObserver(client, { mutationFn: () => Promise.reject(suspended) }).mutate().catch(() => undefined);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["saas", "state"] });
  });
});
