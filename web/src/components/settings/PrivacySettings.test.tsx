import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { scheduleOrgDeletion } from "@/api/privacy";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockPrivacy } from "@/mocks/privacy";
import { DataExportSection } from "./DataExportSection";
import { DeleteOrganizationSection } from "./DeleteOrganizationSection";
import { PersonalDataSection } from "./PersonalDataSection";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("data subject requests", () => {
  afterEach(() => resetMockPrivacy());

  it("requests an organization export with telemetry and lists it", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<DataExportSection />);
    expect(await screen.findByRole("heading", { name: "Data export" })).toBeInTheDocument();
    expect(await screen.findByText("No exports yet.")).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Logs"));
    expect(screen.getByLabelText("From")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Request export" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Export queued");
    expect(await screen.findByText("Ready")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Download the export requested/ })).toBeInTheDocument();
  });

  it("enables organization deletion only after typing the name and the password", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<DeleteOrganizationSection orgName="Default" />);
    const button = await screen.findByRole("button", { name: "Delete organization" });
    expect(button).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Type the organization name (Default) to confirm"), "Default");
    expect(button).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Your password"), MOCK_PASSWORD);
    expect(button).toBeEnabled();
    expect(screen.getByText(/deleted permanently after 7 days/)).toBeInTheDocument();
  });

  it("shows a scheduled organization deletion in the profile and cancels it", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await scheduleOrgDeletion("Default", MOCK_PASSWORD);
    renderWithClient(<PersonalDataSection />);
    const section = (await screen.findByRole("heading", { name: "Organizations scheduled for deletion" })).closest("section")!;
    expect(within(section).getByText(/Default will be deleted permanently/)).toBeInTheDocument();
    await userEvent.click(within(section).getByRole("button", { name: "Cancel the deletion of Default" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Organizations scheduled for deletion" })).not.toBeInTheDocument());

    const del = screen.getByRole("button", { name: "Delete my account" });
    expect(del).toBeDisabled();
    await userEvent.type(screen.getByLabelText(`Type your e-mail address (${MOCK_EMAIL}) to confirm`), MOCK_EMAIL);
    await userEvent.type(screen.getByLabelText("Your password"), MOCK_PASSWORD);
    expect(del).toBeEnabled();
  });
});
