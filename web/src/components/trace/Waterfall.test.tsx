import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Span } from "@/api/types";
import { Waterfall } from "@/components/trace/Waterfall";
import { ThemeProvider } from "@/lib/theme";
import { stubLayout } from "@/test/layout";

const t0 = Date.UTC(2026, 8, 14, 10, 0, 0);
const hex = (n: number) => n.toString(16).padStart(16, "0");
const mk = (n: number, parent: number, name: string, startMs: number): Span => ({
  span_id: hex(n),
  parent_span_id: parent ? hex(parent) : "",
  name,
  kind: parent ? "internal" : "server",
  service_name: n % 2 ? "checkout" : "pricing",
  start: new Date(t0 + startMs).toISOString(),
  duration_ns: 2_000_000,
  status_code: "unset",
  status_message: "",
  attributes: {},
  resource_attributes: {},
  events: [],
});

// root → 50 groups → 100 leaves each: 5051 spans.
function bigTrace(): Span[] {
  const out = [mk(1, 0, "root", 0)];
  let n = 2;
  for (let g = 0; g < 50; g++) {
    const gid = n++;
    out.push(mk(gid, 1, `group-${g}`, g * 10));
    for (let i = 0; i < 100; i++) out.push(mk(n++, gid, `leaf-${g}-${i}`, g * 10 + i / 10));
  }
  return out;
}

function Harness({ spans, onSelect }: { spans: Span[]; onSelect: (id: string) => void }) {
  const [selected, setSelected] = useState<string>();
  return (
    <ThemeProvider>
      <Waterfall
        spans={spans}
        selectedSpanId={selected}
        onSelect={(id) => {
          setSelected(id);
          onSelect(id);
        }}
      />
    </ThemeProvider>
  );
}

const rows = () => screen.getAllByTestId("waterfall-row");

describe("Waterfall", () => {
  const spans = bigTrace();
  beforeEach(() => stubLayout({ viewportHeight: 600, width: 1200, itemHeight: 29 }));
  afterEach(() => vi.restoreAllMocks());

  it("renders a window of rows for thousands of spans as an ARIA tree", () => {
    render(<Harness spans={spans} onSelect={() => undefined} />);
    expect(screen.getByRole("tree", { name: "Span waterfall" })).toBeInTheDocument();
    expect(rows().length).toBeGreaterThan(10);
    expect(rows().length).toBeLessThan(80);
    const root = rows()[0]!;
    expect(root).toHaveAttribute("aria-level", "1");
    expect(root).toHaveAttribute("aria-expanded", "true");
    expect(rows()[1]).toHaveAttribute("aria-setsize", "50");
  });

  it("collapses and expands subtrees with the mouse and keyboard", async () => {
    const user = userEvent.setup();
    render(<Harness spans={spans} onSelect={() => undefined} />);
    // Collapse group-0: its 100 leaves leave the list, group-1 follows it.
    await user.click(within(rows()[1]!).getByTestId("waterfall-toggle"));
    expect(rows()[1]).toHaveAttribute("aria-expanded", "false");
    expect(rows()[1]).toHaveTextContent("+100");
    expect(rows()[2]).toHaveTextContent("group-1");

    await user.click(screen.getByRole("button", { name: "Collapse all" }));
    expect(rows()).toHaveLength(1);

    // ArrowRight on the focused root expands it; ArrowLeft collapses it again.
    await user.click(rows()[0]!);
    await user.keyboard("{ArrowRight}");
    expect(rows()[0]).toHaveAttribute("aria-expanded", "true");
    expect(rows()[1]).toHaveTextContent("group-0");
    expect(rows()[1]).toHaveAttribute("aria-expanded", "false");
    await user.keyboard("{ArrowLeft}");
    expect(rows()).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "Expand all" }));
    expect(rows().length).toBeGreaterThan(20);
    expect(rows()[1]).toHaveAttribute("aria-expanded", "true");
  });

  it("searches, highlights and reveals matches inside collapsed subtrees", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Harness spans={spans} onSelect={onSelect} />);
    await user.click(screen.getByRole("button", { name: "Collapse all" }));
    const box = screen.getByRole("searchbox", { name: "Search spans" });
    await user.type(box, "LEAF-0-4");
    // leaf-0-4 and leaf-0-40..49
    expect(await screen.findByTestId("waterfall-match-count")).toHaveTextContent("– / 11");
    await user.keyboard("{Enter}");
    expect(onSelect).toHaveBeenLastCalledWith(spans.find((s) => s.name === "leaf-0-4")!.span_id);
    expect(screen.getByTestId("waterfall-match-count")).toHaveTextContent("1 / 11");
    // Root and group-0 were expanded to show the match.
    const selected = rows().find((r) => r.getAttribute("aria-selected") === "true")!;
    expect(selected).toHaveTextContent("leaf-0-4");
    expect(selected.querySelector("mark")).toHaveTextContent("leaf-0-4");
    expect(rows()[1]).toHaveAttribute("aria-expanded", "true");
    expect(rows()[1]).toHaveTextContent("group-0");
    expect(rows().length).toBeLessThan(80);

    await user.click(screen.getByRole("button", { name: "Previous match" }));
    expect(screen.getByTestId("waterfall-match-count")).toHaveTextContent("11 / 11");
    expect(onSelect).toHaveBeenLastCalledWith(spans.find((s) => s.name === "leaf-0-49")!.span_id);
  });
});
