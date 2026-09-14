// Virtualized DOM waterfall renderer (TanStack Virtual; fixed row height, so
// 10k+ spans render only the visible rows). Layout math is in lib/waterfall.ts.
// One scroll container scrolls both ways: the time axis sticks to the top and the
// span-name column sticks to the left. Containers below 640px (phones) fit the whole
// timeline with a narrower name column; the axis picks its tick count from its width.
// Subtrees collapse (chevron, ArrowLeft/ArrowRight); search highlights matching spans
// and steps through them, expanding collapsed ancestors of the match it lands on.
import { useVirtualizer } from "@tanstack/react-virtual";
import { AlertCircle, ChevronDown, ChevronRight, ChevronsDownUp, ChevronsUpDown, ChevronUp, Search } from "lucide-react";
import { useDeferredValue, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { Span } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { formatDurationNs } from "@/lib/format";
import { isCompact, useElementWidth } from "@/lib/media";
import { useTheme } from "@/lib/theme";
import { colorFor, cn } from "@/lib/utils";
import {
  expandAncestors,
  layoutWaterfall,
  matchRowIndexes,
  tickAnchor,
  tickCountForWidth,
  timeTicks,
  visibleRowIndexes,
  waterfallTree,
  type WaterfallLayout,
} from "@/lib/waterfall";

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

function Highlight({ text, needle }: { text: string; needle: string }) {
  const lower = text.toLowerCase();
  const at = needle && lower.length === text.length ? lower.indexOf(needle) : -1;
  if (at < 0) return <>{text}</>;
  return (
    <>
      {text.slice(0, at)}
      <mark className="rounded-sm bg-yellow-200 text-foreground dark:bg-yellow-700/60">{text.slice(at, at + needle.length)}</mark>
      {text.slice(at + needle.length)}
    </>
  );
}

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
  const countId = useId();
  const rows = layout.rows;
  const tree = useMemo(() => waterfallTree(rows), [rows]);
  const rowIndexById = useMemo(() => new Map(rows.map((r, i) => [r.span.span_id, i])), [rows]);
  const hasSubtrees = useMemo(() => rows.some((r) => r.childCount > 0), [rows]);

  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const [query, setQuery] = useState("");
  const needle = useDeferredValue(query).trim().toLowerCase();

  const visible = useMemo(() => visibleRowIndexes(rows, collapsed), [rows, collapsed]);
  const visiblePos = useMemo(() => {
    const pos = new Int32Array(rows.length).fill(-1);
    visible.forEach((ri, vi) => (pos[ri] = vi));
    return pos;
  }, [rows, visible]);
  const matches = useMemo(() => matchRowIndexes(rows, needle), [rows, needle]);
  const matchSet = useMemo(() => new Set(matches), [matches]);

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual is not compiler-compatible yet
  const virtualizer = useVirtualizer({
    count: visible.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_PX,
    overscan: 20,
    // The sticky time axis sits above the rows inside the scroll container.
    scrollMargin: AXIS_PX,
    getItemKey: (i) => rows[visible[i]!]!.span.span_id,
  });

  const selectedRow = selectedSpanId !== undefined ? (rowIndexById.get(selectedSpanId) ?? -1) : -1;
  // A selected span inside a collapsed subtree is represented by its closest visible ancestor.
  let activeRow = selectedRow;
  while (activeRow >= 0 && visiblePos[activeRow] === -1) activeRow = tree.parent[activeRow]!;
  const activeIdx = activeRow >= 0 ? visiblePos[activeRow]! : 0;
  const items = virtualizer.getVirtualItems();
  const activeRendered = items.some((vi) => vi.index === activeIdx);
  const currentMatch = selectedRow >= 0 ? matches.indexOf(selectedRow) : -1;

  /**
   * Scrolls visible row `index` into view. Rows have a fixed height below the sticky axis, so the offset is
   * exact. (virtualizer.scrollToIndex right after a collapse that shrank a deeply scrolled list works from
   * stale size/offset state and can resolve to the top instead of the row.)
   */
  const scrollToRow = (index: number, align: "center" | "auto") => {
    const el = scrollRef.current;
    if (!el) return;
    const top = AXIS_PX + index * ROW_PX;
    const view = el.clientHeight;
    let next = el.scrollTop;
    if (align === "center") next = top - (view - ROW_PX) / 2;
    else if (top < el.scrollTop + AXIS_PX) next = top - AXIS_PX;
    else if (top + ROW_PX > el.scrollTop + view) next = top + ROW_PX - view;
    el.scrollTop = Math.max(0, Math.min(next, el.scrollHeight - view));
  };

  // Bring a deep-linked span into view once.
  useEffect(() => {
    if (selectedRow > 0) scrollToRow(visiblePos[selectedRow]!, "center");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows]);

  // A search result revealed by expanding its ancestors is scrolled to once the new rows are laid out.
  const pendingScroll = useRef<string | null>(null);
  useLayoutEffect(() => {
    const id = pendingScroll.current;
    const ri = id !== null ? rowIndexById.get(id) : undefined;
    if (ri === undefined || visiblePos[ri] === -1) return;
    pendingScroll.current = null;
    scrollToRow(visiblePos[ri]!, "center");
  }, [visiblePos, rowIndexById]);

  const toggle = (spanId: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (!next.delete(spanId)) next.add(spanId);
      return next;
    });

  const focusRow = (index: number) => {
    const row = rows[visible[index]!];
    if (!row) return;
    scrollToRow(index, "auto");
    // Wait for the virtualizer to render the row before focusing it.
    requestAnimationFrame(() => requestAnimationFrame(() => document.getElementById(`span-${row.span.span_id}`)?.focus({ preventScroll: true })));
  };

  const goToMatch = (dir: 1 | -1) => {
    const m = matches.length;
    if (m === 0) return;
    let k: number;
    if (currentMatch >= 0) {
      k = (currentMatch + dir + m) % m;
    } else if (dir === 1) {
      k = matches.findIndex((ri) => ri > selectedRow);
      if (k < 0) k = 0;
    } else {
      k = m - 1;
      while (k > 0 && matches[k]! >= selectedRow && selectedRow >= 0) k--;
    }
    const ri = matches[k]!;
    const id = rows[ri]!.span.span_id;
    onSelect(id);
    const next = expandAncestors(rows, tree, ri, collapsed);
    if (next !== collapsed) {
      pendingScroll.current = id;
      setCollapsed(next);
    } else {
      scrollToRow(visiblePos[ri]!, "center");
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const last = visible.length - 1;
    const ri = visible[activeIdx];
    if (ri === undefined) return;
    const row = rows[ri]!;
    const hasKids = row.childCount > 0;
    const isCollapsed = hasKids && collapsed.has(row.span.span_id);
    let next: number | null = null;
    if (e.key === "ArrowDown") next = Math.min(last, activeIdx + 1);
    else if (e.key === "ArrowUp") next = Math.max(0, activeIdx - 1);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = last;
    else if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      if (e.key === "ArrowRight" ? isCollapsed : hasKids && !isCollapsed) {
        toggle(row.span.span_id);
        return;
      }
      if (e.key === "ArrowRight" && hasKids) next = Math.min(last, activeIdx + 1);
      else if (e.key === "ArrowLeft" && tree.parent[ri]! >= 0) next = visiblePos[tree.parent[ri]!]!;
    }
    if (next === null || next < 0) return;
    e.preventDefault();
    onSelect(rows[visible[next]!]!.span.span_id);
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
      <div className="mb-2 flex flex-wrap items-center gap-2" data-testid="waterfall-toolbar">
        <div className="relative min-w-0 flex-1 basis-48">
          <Search className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
          <Input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              e.preventDefault();
              goToMatch(e.shiftKey ? -1 : 1);
            }}
            aria-label={t("trace.search")}
            aria-describedby={countId}
            placeholder={t("trace.searchPlaceholder")}
            className="h-8 pl-7 text-xs"
          />
        </div>
        <span id={countId} role="status" className="font-mono whitespace-nowrap text-muted-foreground" data-testid="waterfall-match-count">
          {needle ? (matches.length > 0 ? t("trace.matches", { current: currentMatch >= 0 ? currentMatch + 1 : "–", total: matches.length }) : t("trace.noMatches")) : ""}
        </span>
        <div className="flex items-center gap-1">
          <Button type="button" variant="outline" size="sm" onClick={() => goToMatch(-1)} disabled={matches.length === 0} aria-label={t("trace.prevMatch")} title={t("trace.prevMatch")}>
            <ChevronUp aria-hidden="true" />
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={() => goToMatch(1)} disabled={matches.length === 0} aria-label={t("trace.nextMatch")} title={t("trace.nextMatch")}>
            <ChevronDown aria-hidden="true" />
          </Button>
        </div>
        {hasSubtrees && (
          <div className="flex items-center gap-1">
            <Button type="button" variant="outline" size="sm" onClick={() => setCollapsed(new Set())} disabled={collapsed.size === 0} title={t("trace.expandAll")}>
              <ChevronsUpDown aria-hidden="true" />
              <span className="max-sm:sr-only">{t("trace.expandAll")}</span>
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setCollapsed(new Set(rows.filter((r) => r.childCount > 0).map((r) => r.span.span_id)))}
              title={t("trace.collapseAll")}
            >
              <ChevronsDownUp aria-hidden="true" />
              <span className="max-sm:sr-only">{t("trace.collapseAll")}</span>
            </Button>
          </div>
        )}
      </div>
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
            role="tree"
            aria-label={t("trace.waterfall")}
            onKeyDown={onKeyDown}
            // When the active row is scrolled out of the DOM, the tree itself takes focus and forwards it.
            tabIndex={activeRendered ? -1 : 0}
            onFocus={(e) => e.target === listRef.current && focusRow(activeIdx)}
            className="relative"
            style={{ height: virtualizer.getTotalSize() }}
          >
            {items.map((vi) => {
              const ri = visible[vi.index]!;
              const row = rows[ri]!;
              const s = row.span;
              const selected = s.span_id === selectedSpanId;
              const color = colorFor(s.service_name, resolved);
              const isError = s.status_code === "error";
              const hasKids = row.childCount > 0;
              const isCollapsed = hasKids && collapsed.has(s.span_id);
              const matched = matchSet.has(ri);
              return (
                <div
                  key={vi.key}
                  id={`span-${s.span_id}`}
                  role="treeitem"
                  aria-level={row.depth + 1}
                  aria-expanded={hasKids ? !isCollapsed : undefined}
                  aria-selected={selected}
                  aria-setsize={tree.setSize[ri]}
                  aria-posinset={tree.posInSet[ri]}
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
                  data-row={ri}
                  data-match={matched || undefined}
                >
                  <div
                    className={cn("sticky left-0 z-10 flex h-full min-w-0 items-center gap-1.5 bg-card pr-2 group-hover:bg-muted", selected && "bg-accent group-hover:bg-accent")}
                    style={{ paddingLeft: row.depth * INDENT_PX }}
                  >
                    {hasKids ? (
                      // Mouse affordance only: keyboard users expand/collapse with ArrowRight/ArrowLeft on the tree item.
                      <button
                        type="button"
                        tabIndex={-1}
                        aria-hidden="true"
                        onClick={(e) => {
                          e.stopPropagation();
                          toggle(s.span_id);
                        }}
                        className="flex size-4 shrink-0 items-center justify-center rounded hover:bg-accent"
                        data-testid="waterfall-toggle"
                      >
                        {isCollapsed ? <ChevronRight className="size-3.5" /> : <ChevronDown className="size-3.5" />}
                      </button>
                    ) : (
                      <span className="inline-block size-4 shrink-0" aria-hidden="true" />
                    )}
                    <span className="inline-block h-3 w-1 shrink-0 rounded-sm" style={{ background: color }} aria-hidden="true" />
                    {isError && <AlertCircle className="size-3.5 shrink-0 text-destructive-text" aria-hidden="true" />}
                    <span className="truncate font-medium">
                      <Highlight text={s.name} needle={needle} />
                    </span>
                    {/* Phones: the color bar and legend identify the service; the span name gets the room. */}
                    {!compact && (
                      <span className="truncate text-muted-foreground">
                        <Highlight text={s.service_name} needle={needle} />
                      </span>
                    )}
                    {isCollapsed && (
                      <span className="shrink-0 rounded bg-muted px-1 font-mono text-muted-foreground" title={t("trace.hiddenChildren", { count: row.childCount })}>
                        +{row.childCount}
                      </span>
                    )}
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
