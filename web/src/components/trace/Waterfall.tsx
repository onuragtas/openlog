// Virtualized DOM waterfall renderer (TanStack Virtual; fixed row height, so
// 10k+ spans render only the visible rows). Layout math is in lib/waterfall.ts.
// One scroll container scrolls both ways: the time axis sticks to the top and the
// span-name column sticks to the left. Containers below 640px (phones) fit the whole
// timeline with a narrower name column; the axis picks its tick count from its width.
import { useVirtualizer } from "@tanstack/react-virtual";
import { AlertCircle } from "lucide-react";
import { useEffect, useMemo, useRef, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { Span } from "@/api/types";
import { formatDurationNs } from "@/lib/format";
import { isCompact, useElementWidth } from "@/lib/media";
import { useTheme } from "@/lib/theme";
import { colorFor, cn } from "@/lib/utils";
import { layoutWaterfall, tickAnchor, tickCountForWidth, timeTicks, type WaterfallLayout } from "@/lib/waterfall";

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
const AXIS_PX = 24;
/** Wide containers: names get 35%; narrower than MIN_WIDTH, the timeline scrolls horizontally. */
const MIN_WIDTH = "40rem";
const GRID = "grid-cols-[minmax(10rem,35%)_1fr]";
/** Containers narrower than this (phones) fit the whole timeline instead of scrolling it sideways. */
const COMPACT_PX = 640;
const COMPACT_MIN_WIDTH = "18rem";
const COMPACT_GRID = "grid-cols-[minmax(7rem,40%)_1fr]";
/** Room per time-axis label (e.g. "999.99 ms" in 12px mono) so neighbours never touch. */
const TICK_LABEL_PX = 80;

export function Waterfall({ spans, selectedSpanId, onSelect, layout: given, maxHeight = "70vh" }: WaterfallProps) {
  const { t } = useTranslation();
  const { resolved } = useTheme();
  const layout = useMemo(() => given ?? layoutWaterfall(spans), [given, spans]);
  const [boxRef, boxWidth] = useElementWidth<HTMLDivElement>();
  const [axisRef, axisWidth] = useElementWidth<HTMLDivElement>();
  const compact = isCompact(boxWidth, COMPACT_PX);
  const grid = compact ? COMPACT_GRID : GRID;
  const ticks = timeTicks(layout.totalNs, tickCountForWidth(axisWidth, TICK_LABEL_PX, 4));
  const scrollRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const rows = layout.rows;

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual is not compiler-compatible yet
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_PX,
    overscan: 20,
    // The sticky time axis sits above the rows inside the scroll container.
    scrollMargin: AXIS_PX,
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
    <div ref={boxRef} className="flex min-w-0 flex-col text-xs">
      <ul className="mb-2 flex flex-wrap gap-x-3 gap-y-1" aria-label={t("trace.fields.service")}>
        {layout.services.map((s) => (
          <li key={s} className="flex min-w-0 items-center gap-1.5">
            <span className="inline-block size-2.5 shrink-0 rounded-sm" style={{ background: colorFor(s, resolved) }} aria-hidden="true" />
            <span className="truncate">{s}</span>
          </li>
        ))}
      </ul>
      <div ref={scrollRef} className="overflow-auto overscroll-contain [scrollbar-gutter:stable]" style={{ maxHeight }} data-testid="waterfall-scroll">
        <div style={{ minWidth: compact ? COMPACT_MIN_WIDTH : MIN_WIDTH }}>
          <div className={cn("sticky top-0 z-20 grid border-b bg-card text-muted-foreground", grid)} style={{ height: AXIS_PX }} aria-hidden="true">
            <span className="sticky left-0 z-10 bg-card" />
            <div ref={axisRef} className="relative mr-10 h-4 self-center" data-testid="waterfall-axis">
              {ticks.map((tk, i) => {
                // The first label starts at its tick and the last ends at it, so neither leaves the axis.
                const anchor = tickAnchor(i, ticks.length);
                const pct = (tk / Math.max(1, layout.totalNs)) * 100;
                return (
                  <span
                    key={i}
                    className={cn("absolute font-mono whitespace-nowrap", anchor === "middle" && "-translate-x-1/2")}
                    style={anchor === "end" ? { right: 0 } : { left: `${pct}%` }}
                    data-anchor={anchor}
                  >
                    {formatDurationNs(Math.round(tk))}
                  </span>
                );
              })}
            </div>
          </div>
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
                    "group absolute top-0 left-0 grid w-full cursor-pointer items-center border-b border-border/50 bg-card hover:bg-muted",
                    grid,
                    selected && "bg-accent hover:bg-accent",
                  )}
                  style={{ height: ROW_PX, transform: `translateY(${vi.start - AXIS_PX}px)` }}
                  data-testid="waterfall-row"
                >
                  <div
                    className={cn("sticky left-0 z-10 flex h-full min-w-0 items-center gap-1.5 bg-card pr-2 group-hover:bg-muted", selected && "bg-accent group-hover:bg-accent")}
                    style={{ paddingLeft: row.depth * INDENT_PX }}
                  >
                    <span className="inline-block h-3 w-1 shrink-0 rounded-sm" style={{ background: color }} aria-hidden="true" />
                    {isError && <AlertCircle className="size-3.5 shrink-0 text-destructive-text" aria-hidden="true" />}
                    <span className="truncate font-medium">{s.name}</span>
                    {/* Phones: the color bar and legend identify the service; the span name gets the room. */}
                    {!compact && <span className="truncate text-muted-foreground">{s.service_name}</span>}
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
    </div>
  );
}
