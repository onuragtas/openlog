import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { api } from "@/api/client";
import { observeServerVersion, resetServerVersion, VERSION_HEADER } from "@/lib/server-version";
import { MOCK_DEFAULT_ORG_ID, MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { server } from "@/mocks/server";
import { DISMISSED_UPDATE_STORAGE, UpdateBanner } from "./UpdateBanner";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

const available = {
  version: "0.9.0",
  commit: "abc",
  date: "",
  latest_available: { version: "0.9.1", notes_url: "https://github.com/onuragtas/openlog/releases/tag/v0.9.1", checked_at: "2026-09-13T03:00:00Z" },
  update_check: "enabled",
  updater: null,
};

describe("UpdateBanner", () => {
  beforeEach(() => {
    resetServerVersion();
    server.use(http.get("*/api/v1/version", () => HttpResponse.json(available, { headers: { [VERSION_HEADER]: "0.9.0" } })));
  });
  afterEach(() => resetServerVersion());

  it("shows an available release to admins until dismissed for that version", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const { unmount } = renderWithClient(<UpdateBanner />);

    expect(await screen.findByText("openlog 0.9.1 is available.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Release notes" })).toHaveAttribute("href", available.latest_available.notes_url);
    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("openlog 0.9.1 is available.")).not.toBeInTheDocument();
    expect(localStorage.getItem(DISMISSED_UPDATE_STORAGE)).toBe("0.9.1");

    unmount();
    renderWithClient(<UpdateBanner />);
    await act(() => new Promise((r) => setTimeout(r, 50)));
    expect(screen.queryByText("openlog 0.9.1 is available.")).not.toBeInTheDocument();
  });

  it("hides the release banner from viewers", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID); // the mock user is a viewer there
    renderWithClient(<UpdateBanner />);
    await act(() => new Promise((r) => setTimeout(r, 50)));
    expect(screen.queryByText(/is available/)).not.toBeInTheDocument();
  });

  it("follows update_requests.can_request: superadmins with any role see it, admins without the permission do not", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const requests = { updater_listening: true, updater_polled_at: null, latest: null };
    server.use(http.get("*/api/v1/version", () => HttpResponse.json({ ...available, update_requests: { ...requests, can_request: true } })));
    setSelectedOrg(MOCK_STAGING_ORG_ID); // viewer there, but a superadmin of a sign-up installation
    const first = renderWithClient(<UpdateBanner />);
    expect(await screen.findByText("openlog 0.9.1 is available.")).toBeInTheDocument();
    first.unmount();

    server.use(http.get("*/api/v1/version", () => HttpResponse.json({ ...available, update_requests: { ...requests, can_request: false } })));
    setSelectedOrg(MOCK_DEFAULT_ORG_ID); // owner, but not a server operator
    renderWithClient(<UpdateBanner />);
    await act(() => new Promise((r) => setTimeout(r, 50)));
    expect(screen.queryByText(/is available/)).not.toBeInTheDocument();
  });

  it("asks to reload when API responses come from a new backend version", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<UpdateBanner />);
    await screen.findByText("openlog 0.9.1 is available.");

    // The API client records the header of every response.
    server.use(http.get("*/api/v1/version", () => HttpResponse.json(available, { headers: { [VERSION_HEADER]: "0.9.1" } })));
    await act(async () => {
      await api.GET("/api/v1/version");
    });
    expect(await screen.findByText("openlog was updated to 0.9.1.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });
});

describe("observeServerVersion", () => {
  afterEach(() => resetServerVersion());
  it("keeps the first version as baseline and reports the first different one", async () => {
    const { getUpdatedServerVersion } = await import("@/lib/server-version");
    observeServerVersion(null);
    observeServerVersion("0.9.0");
    observeServerVersion("0.9.0");
    expect(getUpdatedServerVersion()).toBeNull();
    observeServerVersion("0.9.1");
    observeServerVersion("0.9.0"); // rolling update: still prompts
    expect(getUpdatedServerVersion()).toBe("0.9.1");
  });
});
