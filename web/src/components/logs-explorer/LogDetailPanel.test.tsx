import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { LogQueryRow } from "@/api/explorer";
import { LogDetailPanel } from "./LogDetailPanel";

const row = (id: string, body: string): LogQueryRow => ({
  id,
  timestamp: "2026-10-11T09:00:00.000000000Z",
  observed_timestamp: "2026-10-11T09:00:00.000000000Z",
  severity_text: "ERROR",
  severity_number: 17,
  body,
  service_name: "checkout",
  host_id: "h1",
  host_name: "web-1",
  trace_id: "",
  span_id: "",
  fields: {},
  attributes: { "http.method": "POST" },
  resource_attributes: {},
});

function renderPanel(over: Partial<Parameters<typeof LogDetailPanel>[0]> = {}) {
  const onStep = vi.fn();
  render(
    <LogDetailPanel
      row={row("r2", "second")}
      onClose={vi.fn()}
      columns={["timestamp", "body"]}
      onToggleColumn={vi.fn()}
      onFilter={vi.fn()}
      keyTypes={new Map()}
      canFilter
      position={{ index: 2, total: 3 }}
      onStep={onStep}
      {...over}
    />,
  );
  return { onStep };
}

describe("LogDetailPanel", () => {
  it("steps to the record before and after the open one", async () => {
    const user = userEvent.setup();
    const { onStep } = renderPanel();

    expect(screen.getByText("2 / 3")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Next record" }));
    expect(onStep).toHaveBeenCalledWith(1);
    await user.click(screen.getByRole("button", { name: "Previous record" }));
    expect(onStep).toHaveBeenCalledWith(-1);
  });

  it("stops at both ends rather than wrapping around", async () => {
    const user = userEvent.setup();
    const { onStep } = renderPanel({ position: { index: 1, total: 3 } });

    const prev = screen.getByRole("button", { name: "Previous record" });
    expect(prev).toBeDisabled();
    await user.click(prev);
    expect(onStep).not.toHaveBeenCalled();

    // The oldest loaded record: "load more" is what comes after it, not the newest one again.
    renderPanel({ position: { index: 3, total: 3 } });
    expect(screen.getAllByRole("button", { name: "Next record" }).at(-1)).toBeDisabled();
  });

  it("walks with the arrow keys, but not while a field is being typed into", async () => {
    const user = userEvent.setup();
    const { onStep } = renderPanel();

    await user.keyboard("{ArrowDown}");
    expect(onStep).toHaveBeenCalledWith(1);
    await user.keyboard("{ArrowUp}");
    expect(onStep).toHaveBeenCalledWith(-1);

    // The field search is an input: there the arrows move the caret, as they do everywhere else.
    onStep.mockClear();
    await user.click(screen.getByPlaceholderText("Filter fields…"));
    await user.keyboard("{ArrowDown}");
    expect(onStep).not.toHaveBeenCalled();
  });

  it("is a plain panel when there is nothing to step through", () => {
    renderPanel({ position: undefined, onStep: undefined });

    expect(screen.queryByRole("button", { name: "Next record" })).not.toBeInTheDocument();
    expect(screen.getByText("second")).toBeInTheDocument();
  });
});
