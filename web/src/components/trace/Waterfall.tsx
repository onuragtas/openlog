// Virtualized DOM waterfall renderer (TanStack Virtual; fixed row height, so
// 10k+ spans render only the visible rows). Layout math is in lib/waterfall.ts.
import { useVirtualizer } from "@tanstack/react-virtual";
import { AlertCircle } from "lucide-react";
import { useEffect, useMemo, useRef, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { Span } from "@/api/types";
import { formatDurationNs } from "@/lib/format";
import { useTheme } from "@/lib/theme";
import { colorFor, cn } from "@/lib/utils";
import { layoutWaterfall, timeTicks, type WaterfallLayout } from "@/lib/waterfall";

export interface WaterfallProps {
  spans: Span[];
  selectedSpanId?: string;
  onSelect: (spanId: string) => void;
  /** Precomputed layout of `spans` (avoids computing it twice for big traces). */
  layout?: WaterfallLayout;
  /** Max height of the scrolling row area. */
  maxHeight?: string;
}

const INDENT_PX = 14;
const ROW_PX = 29;
const GRID = "grid-cols-[minmax(10rem,35%)_1fr]";

export function Waterfall({ spans, selectedSpanId, onSelect, layout: given, maxHeight = "70vh" }: WaterfallProps) {
  const { t } = useTranslation();
  const { resolved } = useTheme();
  const layout = useMemo(() => given ?? layoutWaterfall(spans), [given, spans]);
  const ticks = timeTicks(layout.totalNs, 4);
  const scrollRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const rows = layout.rows;

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual is not compiler-compatible yet
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_PX,
    overscan: 20,
    getItemKey: (i) => rows[i]!.span.span_id,
  });

  const selectedIdx = useMemo(() => rows.findIndex((r) => r.span.span_id === selectedSpanId), [rows, selectedSpanId]);
  const activeIdx = selectedIdx >= 0 ? selectedIdx : 0;
  const items = virtualizer.getVirtualItems();
  const activeRendered = items.some((vi) => vi.index === activeIdx);

  // Bring a deep-linked span into view once.
  useEffect(() => {
    if (selectedIdx > 0) virtualizer.scrollToIndex(selectedIdx, { align: "center" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows]);

  const focusRow = (index: number) => {
    const row = rows[index];
    if (!row) return;
    virtualizer.scrollToIndex(index, { align: "auto" });
    // Wait for the virtualizer to render the row before focusing it.
    requestAnimationFrame(() => requestAnimationFrame(() => document.getElementById(`span-${row.span.span_id}`)?.focus({ preventScroll: true })));
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const last = rows.length - 1;
    let next: number | null = null;
    if (e.key === "ArrowDown") next = Math.min(last, activeIdx + 1);
    else if (e.key === "ArrowUp") next = Math.max(0, activeIdx - 1);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = last;
    if (next === null || next < 0) return;
    e.preventDefault();
    onSelect(rows[next]!.span.span_id);
    focusRow(next);
  };

  return (
    <div className="flex flex-col text-xs">
      <ul className="mb-2 flex flex-wrap gap-3" aria-label={t("trace.fields.service")}>
        {layout.services.map((s) => (
          <li key={s} className="flex items-center gap-1.5">
            <span className="inline-block size-2.5 rounded-sm" style={{ background: colorFor(s, resolved) }} aria-hidden="true" />
            {s}
          </li>
        ))}
      </ul>
      <div className={cn("grid overflow-hidden border-b pb-1 text-muted-foreground [scrollbar-gutter:stable]", GRID)} aria-hidden="true">
        <span />
        <div className="relative mr-10 h-4">
          {ticks.map((tk, i) => (
            <span
              key={i}
              className="absolute -translate-x-1/2 font-mono first:translate-x-0 last:-translate-x-full"
              style={{ left: `${(tk / Math.max(1, layout.totalNs)) * 100}%` }}
            >
              {formatDurationNs(Math.round(tk))}
            </span>
          ))}
        </div>
      </div>
      <div ref={scrollRef} className="overflow-auto [scrollbar-gutter:stable]" style={{ maxHeight }} data-testid="waterfall-scroll">
        <div
          ref={listRef}
          role="listbox"
          aria-label={t("trace.waterfall")}
          onKeyDown={onKeyDown}
          // When the active row is scrolled out of the DOM, the list itself takes focus and forwards it.
          tabIndex={activeRendered ? -1 : 0}
          onFocus={(e) => e.target === listRef.current && focusRow(activeIdx)}
          className="relative"
          style={{ height: virtualizer.getTotalSize() }}
        >
          {items.map((vi) => {
            const row = rows[vi.index]!;
            const s = row.span;
            const selected = s.span_id === selectedSpanId;
            const color = colorFor(s.service_name, resolved);
            const isError = s.status_code === "error";
            return (
              <div
                key={vi.key}
                id={`span-${s.span_id}`}
                role="option"
                aria-selected={selected}
                aria-setsize={rows.length}
                aria-posinset={vi.index + 1}
                tabIndex={vi.index === activeIdx ? 0 : -1}
                onClick={() => onSelect(s.span_id)}
                onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), onSelect(s.span_id))}
                aria-label={t("trace.spanLabel", { service: s.service_name, name: s.name, duration: formatDurationNs(s.duration_ns) })}
                className={cn(
                  "absolute top-0 left-0 grid w-full cursor-pointer items-center border-b border-border/50 hover:bg-muted/60",
                  GRID,
                  selected && "bg-accent hover:bg-accent",
                )}
                style={{ height: ROW_PX, transform: `translateY(${vi.start}px)` }}
                data-testid="waterfall-row"
              >
                <div className="flex min-w-0 items-center gap-1.5 pr-2" style={{ paddingLeft: row.depth * INDENT_PX }}>
                  <span className="inline-block h-3 w-1 shrink-0 rounded-sm" style={{ background: color }} aria-hidden="true" />
                  {isError && <AlertCircle className="size-3.5 shrink-0 text-destructive-text" aria-hidden="true" />}
                  <span className="truncate font-medium">{s.name}</span>
                  <span className="truncate text-muted-foreground">{s.service_name}</span>
                  {row.orphan && <span className="shrink-0 text-warning-text">({t("trace.orphan")})</span>}
                </div>
                <div className="relative mr-10 h-5">
                  <div
                    className={cn("absolute top-1 h-3 rounded-sm", isError && "ring-1 ring-destructive")}
                    style={{ left: `${row.left * 100}%`, width: `${row.width * 100}%`, background: color, minWidth: 2 }}
                  />
                  <span
                    className="absolute top-0.5 font-mono whitespace-nowrap text-muted-foreground"
                    style={row.left + row.width > 0.8 ? { right: `${(1 - row.left) * 100 + 0.5}%` } : { left: `calc(${(row.left + row.width) * 100}% + 4px)` }}
                  >
                    {formatDurationNs(s.duration_ns)}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
