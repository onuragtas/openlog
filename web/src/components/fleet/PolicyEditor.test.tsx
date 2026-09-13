import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { fleetPolicyQuery, fleetSummaryQuery } from "@/api/fleet";
import { MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID } from "@/mocks/account";
import { resetMockFleet } from "@/mocks/fleet";
import { PolicyEditor } from "./PolicyEditor";
import { RolloutPanel } from "./RolloutPanel";

function Harness({ canManage }: { canManage: boolean }) {
  const policy = useQuery(fleetPolicyQuery());
  const summary = useQuery({ ...fleetSummaryQuery(), refetchInterval: false });
  return (
    <>
      {policy.data && <PolicyEditor policy={policy.data} canManage={canManage} />}
      {summary.data && <RolloutPanel summary={summary.data} canManage={canManage} />}
    </>
  );
}

function renderHarness(canManage: boolean) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Harness canManage={canManage} />
    </QueryClientProvider>,
  );
}

describe("fleet policy and rollout", () => {
  beforeEach(() => resetMockFleet());

  it("validates waves, saves an automatic policy and pauses the new rollout", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const user = userEvent.setup();
    renderHarness(true);

    const waves = await screen.findByLabelText("Waves (%)");
    expect(await screen.findByText(/Update available: \d+ agents are behind 0\.4\.0\./)).toBeInTheDocument();
    expect(screen.getByText("No rollout yet.")).toBeInTheDocument();

    await user.clear(waves);
    await user.type(waves, "50, 20, 100");
    expect(screen.getByText("Each wave must be larger than the previous one.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save policy" })).toBeDisabled();

    await user.clear(waves);
    await user.type(waves, "10, 50, 100");
    await user.click(screen.getByRole("radio", { name: /Automatic/ }));
    await user.click(screen.getByRole("button", { name: "Add window" }));
    const end = screen.getByLabelText("End");
    await user.clear(end);
    await user.type(end, "25:00");
    expect(screen.getByText("Use HH:MM (00:00–24:00).")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove window 1" }));

    await user.click(screen.getByRole("button", { name: "Save policy" }));
    expect(await screen.findByText("Policy saved")).toBeInTheDocument();

    // The mock starts a rollout for the automatic policy.
    expect(await screen.findByRole("heading", { name: "Upgrade to 0.4.0" })).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "Rollout progress" })).toBeInTheDocument();
    // Every summary refresh advances the mock rollout, so the small first wave may already be done.
    expect(screen.getByTestId("rollout-wave")).toHaveTextContent(/Wave [12] of 3/);

    await user.click(screen.getByRole("button", { name: "Pause" }));
    await user.click(screen.getByRole("button", { name: "Confirm pause" }));
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.getAllByText("Paused").length).toBeGreaterThan(0);
  });

  it("is read-only for viewers", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID);
    renderHarness(false);
    expect(await screen.findByLabelText("Waves (%)")).toBeDisabled();
    expect(screen.getByText("Only admins can change the update policy and rollouts.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save policy" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Roll back" })).not.toBeInTheDocument();
  });
});
