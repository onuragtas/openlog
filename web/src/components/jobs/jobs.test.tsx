import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { login } from "@/api/account";
import i18n from "@/i18n";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { MOCK_JOB_IDS, resetMockJobs } from "@/mocks/jobs";
import { cronValid, formatLate, formatSeconds, jobState, pingCommand } from "@/lib/jobs";
import { JobDetail } from "./JobDetail";
import { JobForm } from "./JobForm";
import { JobsList } from "./JobsList";

function renderWithClient(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

afterEach(async () => {
  resetMockJobs();
  await i18n.changeLanguage("en");
});

describe("Job monitoring", () => {
  it("lists monitors with their state and schedule, and opens one", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onOpen = vi.fn();
    renderWithClient(<JobsList onOpen={onOpen} canWrite />);

    const list = await screen.findByTestId("jobs-list");
    expect(within(list).getByText("Nightly backup")).toBeInTheDocument();
    expect(within(list).getByText("0 3 * * * · Europe/Istanbul")).toBeInTheDocument();
    // A monitor past its grace period reads as late, which is what the person needs to see even before the
    // sweeper concludes the run.
    expect(within(list).getByText("Late")).toBeInTheDocument();
    expect(within(list).getByText("OK")).toBeInTheDocument();
    expect(within(list).getByText("every 1h")).toBeInTheDocument();

    await user.click(within(list).getByText("Nightly backup"));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: MOCK_JOB_IDS.ok }));
  });

  it("shows the ping command and the runs of a monitor", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderWithClient(<JobDetail id={MOCK_JOB_IDS.ok} canWrite />);

    await screen.findByTestId("job-detail");
    // The command comes first: a monitor nobody wired up will only ever report "missed".
    const command = screen.getByLabelText("Command") as HTMLInputElement;
    expect(command.value).toContain('/$?" >/dev/null');
    expect(command.value).toContain("olj_");

    await user.click(screen.getByRole("button", { name: "Also report the start" }));
    expect((screen.getByLabelText("Command") as HTMLTextAreaElement).value).toContain("/start");

    const runs = screen.getByTestId("job-runs");
    expect(within(runs).getAllByText("Success").length).toBeGreaterThan(0);
    expect(within(runs).getAllByText("42 GB written").length).toBeGreaterThan(0);
  });

  it("creates a heartbeat monitor and rejects an interval that is too short", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithClient(<JobForm onSaved={onSaved} />);

    await user.type(screen.getByLabelText("Name"), "Queue drain");
    await user.selectOptions(screen.getByLabelText("Schedule type"), "interval");
    expect(screen.queryByLabelText("Cron expression")).not.toBeInTheDocument();

    const interval = screen.getByLabelText("Report at least every (seconds)");
    await user.clear(interval);
    await user.type(interval, "30");
    await user.click(screen.getByRole("button", { name: "Create monitor" }));
    expect(screen.getByText("Enter a number of seconds between 60 and 7776000.")).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();

    await user.clear(interval);
    await user.type(interval, "600");
    await user.click(screen.getByRole("button", { name: "Create monitor" }));

    await vi.waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(onSaved.mock.calls[0]![0]).toMatchObject({
      name: "Queue drain",
      kind: "interval",
      interval_seconds: 600,
      cron: "",
    });
  });

  it("rejects a cron expression that is not five fields", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithClient(<JobForm onSaved={onSaved} />);

    await user.type(screen.getByLabelText("Name"), "Nightly");
    const cron = screen.getByLabelText("Cron expression");
    await user.clear(cron);
    await user.type(cron, "0 3 * *");
    await user.click(screen.getByRole("button", { name: "Create monitor" }));
    expect(screen.getByText("Enter five fields (minute hour day month weekday) or a macro such as @daily.")).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("translates the state labels", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    await i18n.changeLanguage("tr");
    renderWithClient(<JobsList onOpen={vi.fn()} />);

    const list = await screen.findByTestId("jobs-list");
    expect(within(list).getByText("Gecikti")).toBeInTheDocument();
    expect(within(list).getByText("Takvim")).toBeInTheDocument();
  });
});

describe("job helpers", () => {
  it("accepts the crontab grammar and rejects what the server would", () => {
    for (const expr of ["* * * * *", "0 3 * * *", "*/15 * * * *", "@daily", "@hourly", "0 0 * * mon-fri", "5,35 9-17 * * *"]) {
      expect(cronValid(expr), expr).toBe(true);
    }
    for (const expr of ["", "0 3 * *", "60 * * * *", "* 24 * * *", "@never", "a * * * *", "*/0 * * * *", "5-1 * * * *"]) {
      expect(cronValid(expr), expr).toBe(false);
    }
  });

  it("formats durations and lateness the way the tables read them", () => {
    expect(formatSeconds(3600)).toBe("1h");
    expect(formatSeconds(900)).toBe("15m");
    expect(formatSeconds(45)).toBe("45s");
    expect(formatLate(0)).toBe("0s");
    expect(formatLate(120)).toBe("+2m");
    expect(formatLate(-30)).toBe("−30s");
  });

  it("reads a disabled monitor as paused and a late one as late", () => {
    const state = { status: "success", late: false } as never;
    expect(jobState({ enabled: false, state })).toBe("paused");
    expect(jobState({ enabled: true, state })).toBe("ok");
    expect(jobState({ enabled: true, state: { status: "success", late: true } as never })).toBe("late");
  });

  it("builds a crontab line that reports the exit status", () => {
    expect(pingCommand("https://openlog.example.com/api/v1/jobs/ping/olj_x")).toBe(
      'curl -fsS -m 10 --retry 3 "https://openlog.example.com/api/v1/jobs/ping/olj_x/$?" >/dev/null',
    );
  });
});
