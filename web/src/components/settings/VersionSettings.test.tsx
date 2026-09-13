import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { server } from "@/mocks/server";
import { VersionSettings } from "./VersionSettings";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("VersionSettings", () => {
  it("shows the running version, the newer release and the updater progress", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({
          version: "0.9.0",
          commit: "abc123",
          date: "2026-09-13T09:00:00Z",
          latest_available: { version: "0.9.1", notes_url: "https://example.test/v0.9.1", checked_at: "2026-09-13T10:00:00Z" },
          update_check: "enabled",
          updater: {
            engine: "compose",
            mode: "auto",
            state: "updating",
            current_version: "0.9.0",
            target_version: "0.9.1",
            checked_at: "2026-09-13T10:00:00Z",
            steps: [
              { name: "backup", status: "ok", started_at: "2026-09-13T10:00:00Z" },
              { name: "pull", status: "running", started_at: "2026-09-13T10:00:05Z" },
            ],
          },
        }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);

    expect(await screen.findByTestId("server-version")).toHaveTextContent("0.9.0");
    expect(screen.getByText("abc123")).toBeInTheDocument();
    expect(screen.getByText("Enabled")).toBeInTheDocument();
    expect(within(screen.getByTestId("latest-release")).getByText("0.9.1")).toBeInTheDocument();
    const updater = screen.getByTestId("updater-status");
    expect(within(updater).getByText("updating")).toBeInTheDocument();
    expect(within(updater).getByText("auto")).toBeInTheDocument();
    const steps = within(updater).getByRole("list", { name: "Update steps" });
    expect(within(steps).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["backup", "pull"]);
  });

  it("says up to date without a newer release and no updater", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({ version: "0.9.1", commit: "unknown", date: "", latest_available: null, update_check: "enabled", updater: null }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);

    expect(await screen.findByTestId("server-version")).toHaveTextContent("0.9.1");
    expect(screen.queryByText("unknown")).not.toBeInTheDocument();
    expect(screen.getByTestId("latest-release")).toHaveTextContent("Up to date");
    expect(screen.getByTestId("updater-status")).toHaveTextContent("No updater is reporting");
  });
});
