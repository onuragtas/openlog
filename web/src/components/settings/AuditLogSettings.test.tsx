import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { AuditLogSettings } from "./AuditLogSettings";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

const rows = () => screen.getAllByRole("row").slice(1);

describe("AuditLogSettings", () => {
  it("lists events newest first and loads more pages", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<AuditLogSettings pageSize={10} />);

    expect(await screen.findByText("t-60")).toBeInTheDocument();
    expect(rows()).toHaveLength(10);
    expect(within(rows()[0]!).getByText("license_key.create")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText("t-50")).toBeInTheDocument();
    expect(rows()).toHaveLength(20);
  });

  it("filters by action, actor and period", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<AuditLogSettings />);
    expect(await screen.findByText("t-60")).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Action"), "Members");
    await user.click(screen.getByRole("button", { name: "Apply" }));
    await screen.findByText("t-59");
    expect(rows().every((r) => within(r).queryByText("member.role_change") !== null)).toBe(true);

    await user.type(screen.getByLabelText("Actor"), "nobody@");
    await user.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("No events match the filters.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Reset" }));
    expect(await screen.findByText("t-60")).toBeInTheDocument();
    // Events older than a day disappear with "Last 24 hours" (mock events are one hour apart).
    await user.selectOptions(screen.getByLabelText("Period"), "Last 24 hours");
    await user.click(screen.getByRole("button", { name: "Apply" }));
    await screen.findByText("t-60");
    expect(screen.queryByText("t-30")).not.toBeInTheDocument();
  });

  it("is hidden for roles without audit.read", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // viewer there
    renderWithClient(<AuditLogSettings />);
    expect(await screen.findByText("Only admins and owners can see the audit log.")).toBeInTheDocument();
  });
});
