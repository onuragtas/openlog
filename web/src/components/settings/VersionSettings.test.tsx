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
    expect(within(updater).getByText("Updating")).toBeInTheDocument();
    expect(within(updater).getByText("auto")).toBeInTheDocument();
    const steps = within(updater).getByRole("list", { name: "Update steps" });
    expect(within(steps).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Backup", "Pull image"]);
    // No request channel (static auth mode): no buttons.
    expect(screen.queryByRole("button", { name: "Check now" })).not.toBeInTheDocument();
  });

  it("translates updater messages by code and falls back to the English message", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({
          ...baseVersion,
          updater: {
            ...notifyUpdater,
            state: "rolled_back",
            message: "update to 0.9.1 failed and was rolled back to 0.9.0",
            message_code: "rolled_back",
            message_params: { to: "0.9.1", from: "0.9.0" },
            error: "health: container exited",
            steps: [{ name: "future-step", status: "failed", started_at: "2026-09-13T10:00:00Z" }],
          },
          update_requests: {
            ...baseVersion.update_requests,
            latest: request({
              action: "apply",
              target_version: "0.9.1",
              state: "failed",
              message: "update to 0.9.1 failed and was rolled back to 0.9.0: health: container exited",
              message_code: "rolled_back",
              message_params: { to: "0.9.1", from: "0.9.0", error: "health: container exited" },
            }),
          },
        }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);

    const updater = await screen.findByTestId("updater-status");
    expect(within(updater).getByText("Rolled back")).toBeInTheDocument();
    expect(within(updater).getByText("The update to 0.9.1 failed and was rolled back to 0.9.0.")).toBeInTheDocument();
    expect(within(updater).getByText("health: container exited")).toBeInTheDocument();
    // Unknown step names are shown as they are.
    expect(within(updater).getByText("future-step")).toBeInTheDocument();
    expect(screen.getByTestId("update-request")).toHaveTextContent(
      "The update to 0.9.1 failed and was rolled back to 0.9.0. Error: health: container exited",
    );
  });

  it("shows the English message of documents without or with an unknown code", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({
          ...baseVersion,
          updater: { ...notifyUpdater, message: "openlog 0.9.1 is available (OPENLOG_UPDATER_MODE=notify)" },
          update_requests: {
            ...baseVersion.update_requests,
            latest: request({ state: "done", message: "something new from a newer updater", message_code: "not_known_yet", message_params: {} }),
          },
        }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);

    const updater = await screen.findByTestId("updater-status");
    expect(within(updater).getByText("openlog 0.9.1 is available (OPENLOG_UPDATER_MODE=notify)")).toBeInTheDocument();
    expect(screen.getByTestId("update-request")).toHaveTextContent("something new from a newer updater");
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

  it("shows updater notices, the self-update step and the hints for operators", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({
          ...baseVersion,
          updater: {
            ...notifyUpdater,
            state: "succeeded",
            steps: [
              { name: "health", status: "ok", started_at: "2026-09-13T10:00:00Z" },
              { name: "self-update", status: "failed", detail: "self-test failed: docker: 403", started_at: "2026-09-13T10:01:00Z" },
            ],
            notices: [
              {
                code: "updater_self_update_failed",
                message: "openlog-updater could not replace itself with 0.9.1 …",
                params: { version: "0.9.1", reason: "self-test failed: docker: 403" },
              },
            ],
          },
          // can_request is what the API grants (superadmins on sign-up installations): the hints follow it.
          update_requests: { ...baseVersion.update_requests, updater_listening: false, updater_polled_at: null },
        }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);
    const updater = await screen.findByTestId("updater-status");
    const steps = within(updater).getByRole("list", { name: "Update steps" });
    expect(within(steps).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Health check", "Updater self-update"]);
    expect(within(updater).getByTestId("updater-notice")).toHaveTextContent(
      "openlog-updater could not replace itself with 0.9.1 and keeps running its previous version (re-run install-server.sh to update it): self-test failed: docker: 403",
    );
    expect(screen.getByText(/The updater is not polling for requests/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check now" })).toBeEnabled();
  });

  it("tells operators that the updater is off", async () => {
    server.use(
      http.get("*/api/v1/version", () =>
        HttpResponse.json({
          ...baseVersion,
          updater: { ...notifyUpdater, mode: "off", state: "off", notices: [{ code: "updater_outdated_bundle", message: "x", params: { updater_version: "0.9.0", running_version: "0.9.1" } }] },
        }),
      ),
    );
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    renderWithClient(<VersionSettings />);
    expect(await screen.findByText("The updater runs with OPENLOG_UPDATER_MODE=off.")).toBeInTheDocument();
    expect(screen.getByTestId("updater-notice")).toHaveTextContent("openlog-updater 0.9.0 is older than the running version 0.9.1: re-run install-server.sh to recreate it.");
    expect(screen.queryByRole("button", { name: "Update now" })).not.toBeInTheDocument();
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
