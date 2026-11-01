import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ListPager } from "@/components/ListPager";
import { SortableHead, Table, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { timeOf, useLocalList } from "./list";

interface Row {
  id: string;
  name: string;
  size: number | null;
}

const COLUMNS = { name: (r: Row) => r.name, size: (r: Row) => r.size };

function Rows({ rows }: { rows: Row[] }) {
  const list = useLocalList(rows, COLUMNS, "name");
  return (
    <>
      <Table>
        <TableHeader>
          <TableRow>
            <SortableHead column="name" defaultDir="asc" {...list.head}>
              Name
            </SortableHead>
            <SortableHead column="size" {...list.head}>
              Size
            </SortableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.visible.map((r) => (
            <TableRow key={r.id} data-testid="row">
              <TableCell>{r.id}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <ListPager list={list} count={list.visible.length} hasMore={list.hasMore} />
    </>
  );
}

const order = () => screen.getAllByTestId("row").map((r) => within(r).getByRole("cell").textContent);
const head = (name: string) => screen.getByRole("button", { name });

describe("useLocalList", () => {
  it("starts in the order it was asked for and turns around on the second click", async () => {
    const user = userEvent.setup();
    render(<Rows rows={[{ id: "b", name: "beta", size: 1 }, { id: "a", name: "Alpha", size: 2 }, { id: "c", name: "gamma", size: 3 }]} />);
    expect(order()).toEqual(["a", "b", "c"]);
    expect(screen.getByRole("columnheader", { name: "Name" })).toHaveAttribute("aria-sort", "ascending");
    await user.click(head("Name"));
    expect(order()).toEqual(["c", "b", "a"]);
    expect(screen.getByRole("columnheader", { name: "Name" })).toHaveAttribute("aria-sort", "descending");
  });

  it("starts a column in its own direction", async () => {
    const user = userEvent.setup();
    render(<Rows rows={[{ id: "small", name: "x", size: 1 }, { id: "big", name: "y", size: 2 }]} />);
    await user.click(head("Size"));
    expect(order()).toEqual(["big", "small"]);
  });

  it("sorts numbers as numbers, also inside text", async () => {
    const user = userEvent.setup();
    render(<Rows rows={[{ id: "nine", name: "host-9", size: 9 }, { id: "ten", name: "host-10", size: 10 }, { id: "hundred", name: "host-100", size: 100 }]} />);
    expect(order()).toEqual(["nine", "ten", "hundred"]);
    await user.click(head("Size"));
    expect(order()).toEqual(["hundred", "ten", "nine"]);
    await user.click(head("Size"));
    expect(order()).toEqual(["nine", "ten", "hundred"]);
  });

  it("puts what has no value last in both directions", async () => {
    const user = userEvent.setup();
    render(<Rows rows={[{ id: "none", name: "a", size: null }, { id: "zero", name: "b", size: 0 }, { id: "five", name: "c", size: 5 }]} />);
    await user.click(head("Size"));
    expect(order()).toEqual(["five", "zero", "none"]);
    await user.click(head("Size"));
    expect(order()).toEqual(["zero", "five", "none"]);
  });

  it("hides the pager on a short list and pages a long one", async () => {
    const user = userEvent.setup();
    const many = (n: number): Row[] => Array.from({ length: n }, (_, i) => ({ id: `r${i}`, name: `row ${i}`, size: i }));
    const { unmount } = render(<Rows rows={many(3)} />);
    expect(screen.queryByTestId("list-pager")).toBeNull();
    unmount();
    render(<Rows rows={many(60)} />);
    expect(order()).toHaveLength(50);
    await user.click(screen.getByRole("button", { name: /next/i }));
    expect(order()).toHaveLength(10);
    expect(order()[0]).toBe("r50");
  });

  it("leaves an emptied last page for the one before it", async () => {
    const user = userEvent.setup();
    const many = (n: number): Row[] => Array.from({ length: n }, (_, i) => ({ id: `r${i}`, name: `row ${i}`, size: i }));
    const { rerender } = render(<Rows rows={many(51)} />);
    await user.click(screen.getByRole("button", { name: /next/i }));
    expect(order()).toEqual(["r50"]);
    await act(async () => rerender(<Rows rows={many(50)} />));
    expect(order()).toHaveLength(50);
  });
});

describe("timeOf", () => {
  it("compares instants and not their text, and has nothing for a blank", () => {
    expect(timeOf("2026-01-01T00:00:00.5Z")!).toBeGreaterThan(timeOf("2026-01-01T00:00:00.25Z")!);
    expect(timeOf("2026-01-01T00:00:00.123456789Z")).toBe(Date.parse("2026-01-01T00:00:00.123Z"));
    expect(timeOf(null)).toBeNull();
    expect(timeOf("")).toBeNull();
    expect(timeOf("not a time")).toBeNull();
  });
});
