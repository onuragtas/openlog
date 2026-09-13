import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

const notifyUpdater = {
  engine: "compose",
  mode: "notify",
  state: "available",
  current_version: "0.9.0",
  target_version: "0.9.1",
  checked_at: "2026-09-13T10:00:00Z",
};

const baseVersion = {
  version: "0.9.0",
  commit: "abc123",
  date: "",
  latest_available: { version: "0.9.1", notes_url: "", checked_at: "2026-09-13T10:00:00Z" },
  update_check: "enabled",
  updater: notifyUpdater,
  update_requests: { can_request: true, updater_listening: true, updater_polled_at: "2026-09-13T10:00:00Z", latest: null },
};

function request(over: Record<string, unknown>) {
  return {
    id: "r1",
    action: "check",
    target_version: "",
    ignore_maintenance_window: false,
    state: "pending",
    message: "",
    requested_by_email: "admin@openlog.local",
    requested_at: "2026-09-13T10:00:00Z",
    picked_at: null,
    finished_at: null,
    ...over,
  };
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
          update_requests: null,
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
    // No request channel (static auth mode): no buttons.
    expect(screen.queryByRole("button", { name: "Check now" })).not.toBeInTheDocument();
  });

  it("says up to date without a newer release and no updater", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({ version: "0.9.1", commit: "unknown", date: "", latest_available: null, update_check: "enabled", updater: null, update_requests: null }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);

    expect(await screen.findByTestId("server-version")).toHaveTextContent("0.9.1");
    expect(screen.queryByText("unknown")).not.toBeInTheDocument();
    expect(screen.getByTestId("latest-release")).toHaveTextContent("Up to date");
    expect(screen.getByTestId("updater-status")).toHaveTextContent("No updater is reporting");
  });

  it("checks now and shows the queued request", async () => {
    let checks = 0;
    server.use(
      http.get("*/api/v1/version", () => HttpResponse.json(baseVersion)),
      http.post("*/api/v1/version/check", () => {
        checks++;
        return HttpResponse.json({ ...baseVersion, update_requests: { ...baseVersion.update_requests, latest: request({ state: "pending" }) } });
      }),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<VersionSettings />);

    await user.click(await screen.findByRole("button", { name: "Check now" }));
    const req = await screen.findByTestId("update-request");
    expect(checks).toBe(1);
    expect(req).toHaveTextContent("Waiting for the updater");
    expect(req).toHaveTextContent("Check for updates");
    expect(req).toHaveTextContent("by admin@openlog.local");
  });

  it("shows the rate limit error of a second check", async () => {
    server.use(
      http.get("*/api/v1/version", () => HttpResponse.json(baseVersion)),
      http.post("*/api/v1/version/check", () =>
        HttpResponse.json({ error: { code: "resource_exhausted", message: "a request was made less than 30s ago; retry in 12s" } }, { status: 429, headers: { "Retry-After": "12" } }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<VersionSettings />);
    await user.click(await screen.findByRole("button", { name: "Check now" }));
    expect(await screen.findByText(/retry in 12s/)).toBeInTheDocument();
  });

  it(
    "updates now outside the maintenance window and follows the server restart",
    async () => {
      let phase: "before" | "restarting" | "after" = "before";
      let applyBody: unknown = null;
      server.use(
        http.get("*/api/v1/version", () => {
          if (phase === "restarting") return HttpResponse.error();
          if (phase === "after")
            return HttpResponse.json({
              ...baseVersion,
              version: "0.9.1",
              latest_available: null,
              updater: { ...notifyUpdater, state: "succeeded", current_version: "0.9.1", message: "updated 0.9.0 → 0.9.1" },
              update_requests: {
                ...baseVersion.update_requests,
                latest: request({ action: "apply", target_version: "0.9.1", state: "done", message: "updated 0.9.0 → 0.9.1" }),
              },
            });
          return HttpResponse.json(baseVersion);
        }),
        http.post("*/api/v1/version/update", async ({ request: r }) => {
          applyBody = await r.json();
          phase = "restarting";
          return HttpResponse.json(request({ action: "apply", target_version: "0.9.1", ignore_maintenance_window: true }), { status: 202 });
        }),
      );
      await login(MOCK_EMAIL, MOCK_PASSWORD);
      const user = userEvent.setup();
      renderWithClient(<VersionSettings />);

      await user.click(await screen.findByRole("button", { name: "Update now" }));
      const confirm = screen.getByRole("group", { name: "Update openlog to 0.9.1 now?" });
      await user.click(within(confirm).getByRole("checkbox", { name: "Install now, also outside the maintenance window" }));
      await user.click(within(confirm).getByRole("button", { name: "Start update" }));
      expect(applyBody).toEqual({ target_version: "0.9.1", ignore_maintenance_window: true });

      // The API container is recreated: requests fail, the page keeps the last data and reconnects.
      expect(await screen.findByText("The server is restarting… reconnecting.", {}, { timeout: 6000 })).toBeInTheDocument();
      expect(screen.getByTestId("server-version")).toHaveTextContent("0.9.0");

      phase = "after";
      expect(await screen.findByText("0.9.1", { selector: "[data-testid=server-version]" }, { timeout: 6000 })).toBeInTheDocument();
      expect(screen.queryByText("The server is restarting… reconnecting.")).not.toBeInTheDocument();
      expect(screen.getByTestId("update-request")).toHaveTextContent("Done");
      expect(screen.queryByRole("button", { name: "Update now" })).not.toBeInTheDocument();
    },
    20_000,
  );

  it("disables Update now when the compose updater does not poll requests", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({ ...baseVersion, update_requests: { ...baseVersion.update_requests, updater_listening: false, updater_polled_at: null } }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);
    expect(await screen.findByRole("button", { name: "Update now" })).toBeDisabled();
    expect(screen.getByText(/The updater is not polling for requests/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check now" })).toBeEnabled();
  });

  it("has no buttons for members", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({
          ...baseVersion,
          update_requests: { ...baseVersion.update_requests, can_request: false, latest: request({ requested_by_email: null, state: "running" }) },
        }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);
    expect(await screen.findByTestId("update-request")).toHaveTextContent("In progress");
    expect(screen.getByTestId("update-request")).not.toHaveTextContent("by ");
    expect(screen.queryByRole("button", { name: "Check now" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Update now" })).not.toBeInTheDocument();
  });
});
