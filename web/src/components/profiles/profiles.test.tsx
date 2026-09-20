import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import type { FlameNode } from "@/api/profiles";
import i18n from "@/i18n";
import { formatValue } from "@/lib/profile-format";
import { FlameGraph } from "./FlameGraph";
import { FunctionsTable } from "./FunctionsTable";

// A tree whose widths are round numbers, so the layout is checked by arithmetic rather than by eye: main is
// 80% of the root and db.Query 75% of main, which is 60% of the root.
const FLAME: FlameNode = {
  name: "all",
  value: 1000,
  children: [
    { name: "main", value: 800, children: [{ name: "db.Query", value: 600 }] },
    { name: "runtime.gcBgMarkWorker", value: 200 },
  ],
};

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("profiling", () => {
  it("lays frames out as their share of the parent", () => {
    render(<FlameGraph flame={FLAME} unit="nanoseconds" />);

    const tree = screen.getByRole("tree");
    const main = within(tree).getByRole("treeitem", { name: /^main/ });
    // 800 of 1000 — the width is the share of the value, which is what makes a flame graph readable.
    expect(main).toHaveStyle({ width: "80%" });
    expect(within(tree).getByRole("treeitem", { name: /^db\.Query/ })).toHaveStyle({ width: "60%" });
    // Depth is the stack depth: the root is level 1, its callee 2, its callee 3.
    expect(main).toHaveAttribute("aria-level", "2");
    expect(within(tree).getByRole("treeitem", { name: /^db\.Query/ })).toHaveAttribute("aria-level", "3");
  });

  it("re-roots on the selected frame and back", async () => {
    const user = userEvent.setup();
    render(<FlameGraph flame={FLAME} unit="nanoseconds" />);

    await user.click(screen.getByRole("treeitem", { name: /^main/ }));

    // Zoomed in, main fills the width and the sibling that is not below it is gone.
    expect(screen.getByRole("treeitem", { name: /^main/ })).toHaveStyle({ width: "100%" });
    expect(screen.queryByRole("treeitem", { name: /gcBgMarkWorker/ })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Back to the whole profile" }));
    expect(screen.getByRole("treeitem", { name: /gcBgMarkWorker/ })).toBeInTheDocument();
  });

  it("prints values in the unit the profile declared", () => {
    expect(formatValue(1_500_000, "nanoseconds", "en")).toBe("1.5 ms");
    expect(formatValue(750, "nanoseconds", "en")).toBe("750 ns");
    expect(formatValue(2048, "bytes", "en")).toBe("2 KiB");
    // An unrecognised unit is printed rather than guessed at — never silently rendered as milliseconds.
    expect(formatValue(42, "count", "en")).toBe("42 count");
  });

  it("shares self time against the rows returned", () => {
    render(
      <FunctionsTable
        unit="nanoseconds"
        total={1000}
        functions={[
          { function: "db.Query", self: 600, samples: 12 },
          { function: "json.Marshal", self: 400, samples: 8 },
        ]}
      />,
    );

    const table = screen.getByTestId("profile-functions");
    const row = within(table).getByText("db.Query").closest("tr");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText("600 ns")).toBeInTheDocument();
    expect(within(row as HTMLElement).getByText("60.0%")).toBeInTheDocument();
  });
});
