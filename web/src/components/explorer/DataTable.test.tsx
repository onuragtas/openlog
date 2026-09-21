import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { stubLayout } from "@/test/layout";
import { DataTable } from "./DataTable";

// A live list grows at the top: the newest record is prepended on every refetch. Selecting by position meant
// the open detail panel silently swapped to whatever row had slid into the clicked index — the reason this
// file exists.

interface Row {
  id: string;
  timestamp: string;
  body: string;
}

const ALPHA: Row = { id: "a", timestamp: "2026-09-21T10:00:00.000Z", body: "alpha" };
const BETA: Row = { id: "b", timestamp: "2026-09-21T10:00:01.000Z", body: "beta" };
const GAMMA: Row = { id: "c", timestamp: "2026-09-21T10:00:02.000Z", body: "gamma" };
/** The record that arrives while the panel is open. */
const FRESH: Row = { id: "new", timestamp: "2026-09-21T10:00:03.000Z", body: "fresh" };

function table(rows: Row[], selectedId: string | null, onOpen: (row: Row) => void) {
  return (
    <DataTable<Row>
      rows={rows}
      columns={["timestamp", "body"]}
      onColumnsChange={vi.fn()}
      onOpen={onOpen}
      selectedId={selectedId}
      order="desc"
      density="compact"
      wrap={false}
      ariaLabel="records"
      headerLabel={(c) => c}
      defaultWidths={{ timestamp: 200, body: 400 }}
      openLabel={(time) => `open ${time}`}
      renderCell={(row, c, _i, h) => (c === "timestamp" ? h.timeButton() : h.text(row.body))}
      renderCard={(row, _i, h) => h.text(row.body)}
      cellValue={(row, c) => (c === "body" ? row.body : undefined)}
      loadMoreLabels={{ more: "more", loading: "loading", none: "none" }}
    />
  );
}

const rowFor = (body: string) => screen.getByText(body).closest("[data-index]");

describe("DataTable selection", () => {
  // The rows are virtualized, and jsdom has no layout: without a viewport the list renders nothing at all.
  beforeEach(() => stubLayout({ viewportHeight: 600, width: 1000, itemHeight: 30 }));

  it("opens the row itself, not its position", async () => {
    await i18n.changeLanguage("en");
    const user = userEvent.setup();
    const onOpen = vi.fn();
    render(table([GAMMA, BETA, ALPHA], null, onOpen));

    const row = rowFor("beta");
    expect(row).not.toBeNull();
    await user.click(row as HTMLElement);

    // The whole row, so the caller can keep it: an index would be meaningless the moment the list changes.
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: "b", body: "beta" }));
  });

  it("keeps the selection on the clicked record when a newer one arrives", async () => {
    await i18n.changeLanguage("en");
    const onOpen = vi.fn();
    const { rerender } = render(table([GAMMA, BETA, ALPHA], "b", onOpen));
    expect(rowFor("beta")).toHaveAttribute("data-selected");

    // The refetch prepends a newer record: every position shifts by one.
    rerender(table([FRESH, GAMMA, BETA, ALPHA], "b", onOpen));

    expect(rowFor("beta")).toHaveAttribute("data-selected");
    expect(rowFor("gamma")).not.toHaveAttribute("data-selected");
    expect(rowFor("fresh")).not.toHaveAttribute("data-selected");
  });

  it("keeps nothing selected when the id is not in the list", () => {
    render(table([GAMMA, BETA, ALPHA], "gone", vi.fn()));
    for (const body of ["alpha", "beta", "gamma"]) {
      expect(rowFor(body)).not.toHaveAttribute("data-selected");
    }
  });
});
