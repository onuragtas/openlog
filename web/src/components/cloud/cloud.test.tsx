import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { MOCK_CLOUD_IDS, mockCloudCredentials, resetMockCloud } from "@/mocks/cloud";
import { CloudConnectionDetail } from "./CloudConnectionDetail";
import { CloudConnectionForm } from "./CloudConnectionForm";
import { CloudConnectionsList } from "./CloudConnectionsList";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(async () => {
  resetMockCloud();
  await i18n.changeLanguage("en");
});

describe("Cloud connections", () => {
  it("lists connections with their provider and state, and opens one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    renderWithClient(<CloudConnectionsList onOpen={onOpen} canManage />);

    const list = await screen.findByTestId("cloud-list");
    expect(within(list).getByText("Production AWS")).toBeInTheDocument();
    expect(within(list).getByText("Azure production")).toBeInTheDocument();
    // The AWS account polled both regions cleanly; the Azure subscription's last poll was rejected.
    expect(within(list).getByText("Healthy")).toBeInTheDocument();
    expect(within(list).getByText("Failing")).toBeInTheDocument();
    // The failure is visible without opening the connection.
    expect(within(list).getByText(/not allowed to read these metrics/)).toBeInTheDocument();

    await user.click(within(list).getByRole("button", { name: "Production AWS" }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: MOCK_CLOUD_IDS.aws }));
  });

  it("creates a connection and sends the credentials without ever showing them again", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithClient(<CloudConnectionForm onSaved={onSaved} />);

    await screen.findByTestId("cloud-form");
    await user.type(screen.getByLabelText("Name"), "Staging AWS");
    await user.type(screen.getByLabelText("Regions"), "eu-west-1");
    await user.click(screen.getByLabelText("RDS"));
    await user.type(screen.getByLabelText("Access key ID"), "AKIDEXAMPLE");
    await user.type(screen.getByLabelText("Secret access key"), "top-secret-key");
    await user.click(screen.getByRole("button", { name: "Create connection" }));

    await vi.waitFor(() => expect(onSaved).toHaveBeenCalled());
    const saved = onSaved.mock.calls[0]![0] as { id: string; credentials_set: boolean; scopes: string[] };
    expect(saved).toMatchObject({ credentials_set: true, scopes: ["eu-west-1"] });
    // The secret reached the server, and the response carries only the fact that one is stored.
    expect(mockCloudCredentials(saved.id)).toMatchObject({ secret_access_key: "top-secret-key" });
    expect(JSON.stringify(saved)).not.toContain("top-secret-key");
  });

  it("rejects a connection without a scope or a service before sending it", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithClient(<CloudConnectionForm onSaved={onSaved} />);

    await screen.findByTestId("cloud-form");
    await user.type(screen.getByLabelText("Name"), "Incomplete");
    await user.click(screen.getByRole("button", { name: "Create connection" }));

    expect(screen.getByText("Enter at least one scope; letters, digits, '-', '_' and '.' only.")).toBeInTheDocument();
    expect(screen.getByText("Choose at least one service.")).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("tests the credentials and shows the provider's answer inline", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<CloudConnectionForm />);

    await screen.findByTestId("cloud-form");
    await user.type(screen.getByLabelText("Regions"), "eu-west-1");
    await user.type(screen.getByLabelText("Access key ID"), "AKIDEXAMPLE");
    // The mock rejects this key, so the failure path is what the user sees first.
    await user.type(screen.getByLabelText("Secret access key"), "wrong");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByTestId("cloud-test-error")).toHaveTextContent("the credentials were rejected");

    await user.clear(screen.getByLabelText("Secret access key"));
    await user.type(screen.getByLabelText("Secret access key"), "a-working-key");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByTestId("cloud-test-ok")).toBeInTheDocument();
  });

  it("shows every scope and what the recent polls collected", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<CloudConnectionDetail id={MOCK_CLOUD_IDS.aws} canManage />);

    expect(await screen.findByTestId("cloud-detail")).toBeInTheDocument();
    // Both regions are listed with their own status, which is the point of one schedule row per scope.
    const scopes = screen.getByTestId("cloud-scopes");
    expect(within(scopes).getByText("eu-central-1")).toBeInTheDocument();
    expect(within(scopes).getByText("us-east-1")).toBeInTheDocument();

    const runs = screen.getByTestId("cloud-runs");
    // A poll that hit a cap is reported as partial, with the reason and the per-service breakdown.
    expect(within(runs).getByText("Partial")).toBeInTheDocument();
    expect(within(runs).getByText(/metric cap of this poll was reached/)).toBeInTheDocument();
    expect(within(runs).getByText(/RDS: 330/)).toBeInTheDocument();
  });

  it("Turkish connection states and column headings", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("tr");
    renderWithClient(<CloudConnectionsList onOpen={vi.fn()} />);

    const list = await screen.findByTestId("cloud-list");
    expect(within(list).getByText("Sağlıklı")).toBeInTheDocument();
    expect(within(list).getByText("Başarısız")).toBeInTheDocument();
    expect(within(list).getByText("Sağlayıcı")).toBeInTheDocument();
    expect(within(list).getByText("Son toplama")).toBeInTheDocument();
  });
});
