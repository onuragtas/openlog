import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import type { Onboarding } from "@/api/onboarding";
import { setSupportSession } from "@/api/supportSession";
import type { KeyChoice } from "@/lib/onboarding-key";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { resetMockOperator, setMockOrgSuspended } from "@/mocks/operator";
import { LicenseKeyStep } from "./LicenseKeyStep";

const ONBOARDING: Onboarding = {
  ui_url: { url: "https://openlog.example.com", source: "configured" },
  otlp_http: { url: "https://openlog.example.com:4318", source: "derived_public_url" },
  otlp_grpc: { url: "https://openlog.example.com:4317", source: "derived_public_url" },
  server_version: "0.9.1",
  agent_version: "0.9.1",
  release_channel: "stable",
  cors_enabled: false,
  cors_allowed_origins: [],
  auth_mode: "postgres",
  organization: { id: "org", tenant_id: "default", name: "Default" },
  role: "owner",
  features: { license_keys: true, can_create_license_keys: true, can_list_license_keys: true, fleet_php_install: true, tail_sampling: false },
};

const CREATE: KeyChoice = { mode: "create", pasted: "", pasteFor: "", created: null };
// The radio label wraps its help text, so the accessible name starts with the choice label.
const CREATE_RADIO = /^Create a new key for this install/;

function renderStep() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <LicenseKeyStep onboarding={ONBOARDING} targetTitle="Linux" needed value={CREATE} onChange={() => undefined} /> });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
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

describe("license key step in a read-only organization", { timeout: 20_000 }, () => {
  it("active organization: admins create a key inline", async () => {
    renderStep();
    const create = await screen.findByRole("button", { name: "Create key" });
    expect(create).toBeEnabled();
    expect(screen.getByRole("radio", { name: CREATE_RADIO })).toBeEnabled();
    expect(screen.getByText("The value is shown only once and inserted into the commands.")).toBeInTheDocument();
  });

  it("suspended organization: Create key is disabled with the reason, pasting a key stays available", async () => {
    setMockOrgSuspended("default", true);
    renderStep();
    // The controls re-render inside the guard once the organization state has loaded: query them again.
    const create = () => screen.getByRole("button", { name: "Create key" });
    await waitFor(() => expect(create()).toBeDisabled());
    expect(create().closest("[data-testid=read-only-guard]")).toHaveAttribute("title", expect.stringContaining("suspended"));
    const radio = screen.getByRole("radio", { name: CREATE_RADIO });
    expect(radio).toBeDisabled();
    expect(radio).toHaveAccessibleDescription(/suspended/);
    expect(screen.queryByText("Only admins can create license keys.")).not.toBeInTheDocument();
    // Choices in order: create, paste an existing key, placeholder. Pasting is not a change.
    expect(screen.getAllByRole("radio")[1]).toBeEnabled();
  });

  it("operator support view: Create key is disabled with the reason", async () => {
    setSupportSession({ id: "ss-1", org_id: "o", org_name: "Acme", expires_at: new Date(Date.now() + 3_600_000).toISOString() });
    renderStep();
    const create = () => screen.getByRole("button", { name: "Create key" });
    await waitFor(() => expect(create()).toBeDisabled());
    expect(screen.getByRole("radio", { name: CREATE_RADIO })).toHaveAccessibleDescription(/Support view/);
  });
});
