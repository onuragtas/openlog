// Logs Explorer table: dynamic columns (any key), drag or menu reordering, resizable headers (pointer and keyboard),
// timestamp pinned first, wrap and density options, virtualized rows (TanStack Virtual) with "load more" paging.
// Narrow containers render each record as a card with the extra columns as key=value pairs.
import { Link } from "@tanstack/react-router";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowLeft, ArrowRight, GripVertical, Loader2, MoreHorizontal, Trash2 } from "lucide-react";
import { Popover } from "radix-ui";
import { useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { LogQueryRow } from "@/api/explorer";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatDateTime } from "@/lib/format";
import { cellValue, MAX_COLUMN_WIDTH, MIN_COLUMN_WIDTH, moveColumn, severityBadgeVariant, storedWidths, storeWidths, type TablePrefs } from "@/lib/logs-explorer";
import { isCompact, useElementWidth } from "@/lib/media";
import { severityLabel } from "@/lib/severity";
import { parseTimeParam } from "@/lib/time";
import { cn } from "@/lib/utils";

const HEADER_LABEL = { timestamp: "logs.columns.time", severity_text: "logs.columns.severity", "service.name": "logs.columns.service", body: "logs.columns.body", trace_id: "logs.trace" } as const;
const DEFAULT_WIDTH: Record<string, number> = { timestamp: 200, severity_text: 104, "service.name": 160, body: 480, trace_id: 120 };
const OTHER_WIDTH = 180;
const CARD_BELOW = 640;
const CARD_FIELDS = new Set(["timestamp", "severity_text", "service.name", "body", "trace_id"]);

export interface ExplorerTableProps extends TablePrefs {
  rows: LogQueryRow[];
  columns: string[];
  onColumnsChange: (columns: string[]) => void;
  onOpen: (index: number) => void;
  selectedIndex?: number | null;
  /** Row order: "load more" continues with older (desc) or newer (asc) records. */
  order: "asc" | "desc";
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  maxHeight?: string;
}

function SeverityBadge({ row }: { row: LogQueryRow }) {
  const { t } = useTranslation();
  const label = severityLabel(row.severity_text, row.severity_number);
  return label ? (
    <Badge variant={severityBadgeVariant(row.severity_number)}>{label}</Badge>
  ) : (
    <span className="text-muted-foreground" title={t("logs.severityUnspecified")}>
      —
    </span>
  );
}

function TraceLink({ row }: { row: LogQueryRow }) {
  const { t } = useTranslation();
  return (
    <Link
      to="/traces/$traceId"
      params={{ traceId: row.trace_id }}
      search={row.span_id ? { span: row.span_id } : {}}
      onClick={(e) => e.stopPropagation()}
      className="shrink-0 font-mono text-xs text-primary underline-offset-2 hover:underline pointer-coarse:py-1"
      aria-label={row.span_id ? t("logs.viewSpan", { id: row.span_id }) : t("logs.viewTrace", { id: row.trace_id })}
    >
      {row.trace_id.slice(0, 8)}
    </Link>
  );
}

export function ExplorerTable({ rows, columns, onColumnsChange, onOpen, selectedIndex, order, wrap, density, hasMore, loadingMore, onLoadMore, maxHeight = "min(70vh, 60rem)" }: ExplorerTableProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const scrollRef = useRef<HTMLDivElement>(null);
  const [measureRef, width] = useElementWidth<HTMLDivElement>();
  const cards = isCompact(width, CARD_BELOW);
  const [widths, setWidths] = useState(storedWidths);
  const [drag, setDrag] = useState<{ from: number; over: number } | null>(null);
  const comfortable = density === "comfortable";

  const widthOf = (c: string) => widths[c] ?? DEFAULT_WIDTH[c] ?? OTHER_WIDTH;
  const template = columns.map((c) => (c === "body" && widths.body === undefined ? `minmax(${DEFAULT_WIDTH.body}px, 1fr)` : `${widthOf(c)}px`)).join(" ");
  const minWidth = columns.reduce((n, c) => n + widthOf(c), 0);
  const labelOf = (c: string) => (c in HEADER_LABEL ? t(HEADER_LABEL[c as keyof typeof HEADER_LABEL]) : c);

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual is not compiler-compatible yet
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => (cards ? 88 : comfortable ? 40 : 30),
    overscan: 12,
    // Layout options are part of the key: switching remounts rows so their new heights are measured.
    getItemKey: (i) => `${cards ? "c" : "t"}${wrap ? "w" : ""}${density}|${i}|${rows[i]!.id}`,
  });

  const resize = (c: string, w: number, commit: boolean) =>
    setWidths((prev) => {
      const next = { ...prev, [c]: Math.round(Math.min(MAX_COLUMN_WIDTH, Math.max(MIN_COLUMN_WIDTH, w))) };
      if (commit) storeWidths(next);
      return next;
    });

  const timeButton = (row: LogQueryRow, index: number) => {
    const time = formatDateTime(parseTimeParam(row.timestamp) ?? 0, locale, true);
    return (
      <button
        type="button"
        className="rounded-sm text-left font-mono text-xs whitespace-nowrap hover:underline pointer-coarse:py-1"
        onClick={(e) => {
          e.stopPropagation();
          onOpen(index);
        }}
        aria-label={t("logsExplorer.openDetails", { time })}
      >
        <time dateTime={row.timestamp}>{time}</time>
      </button>
    );
  };

  const text = (value: string, mono = true): ReactNode => (
    <span className={cn(mono && "font-mono", "text-xs", wrap ? "break-all whitespace-pre-wrap" : "block truncate")} title={wrap ? undefined : value}>
      {value}
    </span>
  );

  const cell = (row: LogQueryRow, c: string, index: number): ReactNode => {
    switch (c) {
      case "timestamp":
        return timeButton(row, index);
      case "severity_text":
        return <SeverityBadge row={row} />;
      case "trace_id":
        return row.trace_id ? <TraceLink row={row} /> : null;
      case "body":
        return (
          <span className={cn("flex min-w-0 gap-2", wrap ? "items-start" : "items-center")}>
            {row.trace_id && !columns.includes("trace_id") && <TraceLink row={row} />}
            <span className="min-w-0 flex-1">{text(row.body)}</span>
          </span>
        );
      default: {
        const v = cellValue(row, c);
        return v === undefined ? <span className="text-xs text-muted-foreground">–</span> : text(v, c !== "service.name");
      }
    }
  };

  return (
    <div ref={measureRef} className="text-sm">
      <div ref={scrollRef} role="table" aria-label={t("logs.title")} aria-rowcount={rows.length + 1} className="overflow-auto overscroll-contain" style={{ maxHeight }} data-testid="explorer-scroll">
        <div style={{ minWidth: cards ? undefined : minWidth }}>
          {!cards && (
            <div role="rowgroup" className="sticky top-0 z-10 border-b bg-card">
              <div role="row" aria-rowindex={1} className="grid" style={{ gridTemplateColumns: template }}>
                {columns.map((c, i) => (
                  <div
                    key={c}
                    role="columnheader"
                    title={c}
                    draggable={i > 0}
                    onDragStart={(e) => {
                      e.dataTransfer.effectAllowed = "move";
                      e.dataTransfer.setData("text/plain", c);
                      setDrag({ from: i, over: i });
                    }}
                    onDragOver={(e) => {
                      if (!drag || i === 0) return;
                      e.preventDefault();
                      if (drag.over !== i) setDrag({ ...drag, over: i });
                    }}
                    onDrop={(e) => {
                      e.preventDefault();
                      if (drag) onColumnsChange(moveColumn(columns, drag.from, i));
                      setDrag(null);
                    }}
                    onDragEnd={() => setDrag(null)}
                    className={cn("relative flex h-9 min-w-0 items-center gap-1 pr-3 pl-3 text-xs font-medium text-muted-foreground", i > 0 && "cursor-grab", drag && drag.over === i && drag.from !== i && "bg-accent")}
                  >
                    {i > 0 && <GripVertical className="-ml-2 size-3 shrink-0 opacity-50" aria-hidden="true" />}
                    <span className="min-w-0 truncate">{labelOf(c)}</span>
                    {i > 0 && (
                      <ColumnMenu
                        name={labelOf(c)}
                        canLeft={i > 1}
                        canRight={i < columns.length - 1}
                        onMove={(d) => onColumnsChange(moveColumn(columns, i, i + d))}
                        onRemove={() => onColumnsChange(columns.filter((x) => x !== c))}
                      />
                    )}
                    <ResizeHandle name={labelOf(c)} width={widthOf(c)} onResize={(w, commit) => resize(c, w, commit)} />
                  </div>
                ))}
              </div>
            </div>
          )}
          <div role="rowgroup" className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((vi) => {
              const row = rows[vi.index]!;
              const selected = vi.index === selectedIndex;
              const common = {
                "data-index": vi.index,
                ref: virtualizer.measureElement,
                role: "row",
                "aria-rowindex": vi.index + 2,
                "data-selected": selected || undefined,
                onClick: () => onOpen(vi.index),
                style: { transform: `translateY(${vi.start}px)` },
              };
              if (cards) {
                const extras = columns.filter((c) => !CARD_FIELDS.has(c));
                return (
                  <div key={vi.key} {...common} className={cn("absolute top-0 left-0 w-full cursor-pointer border-b border-border/60 px-3 py-2 hover:bg-muted/50", selected && "bg-accent/60")}>
                    <div role="cell" className="flex min-w-0 flex-col gap-1">
                      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                        {timeButton(row, vi.index)}
                        <SeverityBadge row={row} />
                        {row.service_name && <span className="max-w-full truncate text-muted-foreground">{row.service_name}</span>}
                        {row.trace_id && <TraceLink row={row} />}
                      </div>
                      <span className={cn("font-mono text-xs break-all", wrap ? "whitespace-pre-wrap" : "line-clamp-3")}>{row.body}</span>
                      {extras.length > 0 && (
                        <div className="flex min-w-0 flex-wrap gap-x-3 gap-y-0.5 font-mono text-[11px]">
                          {extras.map((c) => (
                            <span key={c} className="max-w-full truncate text-muted-foreground">
                              {c}=<span className="text-foreground">{cellValue(row, c) ?? "–"}</span>
                            </span>
                          ))}
                        </div>
                      )}
                    </div>
                  </div>
                );
              }
              return (
                <div
                  key={vi.key}
                  {...common}
                  className={cn("absolute top-0 left-0 grid w-full cursor-pointer border-b border-border/60 hover:bg-muted/50", wrap ? "items-start" : "items-center", selected && "bg-accent/60")}
                  style={{ ...common.style, gridTemplateColumns: template }}
                >
                  {columns.map((c) => (
                    <div key={c} role="cell" className={cn("min-w-0 px-3", comfortable ? "py-2" : "py-1")}>
                      {cell(row, c, vi.index)}
                    </div>
                  ))}
                </div>
              );
            })}
          </div>
        </div>
        {onLoadMore && (
          <div className="sticky left-0 flex justify-center border-t p-3">
            {hasMore ? (
              <Button variant="outline" size="sm" onClick={onLoadMore} disabled={loadingMore} aria-live="polite">
                {loadingMore && <Loader2 className="animate-spin" aria-hidden="true" />}
                {order === "asc" ? (loadingMore ? t("logsExplorer.loadingNewer") : t("logsExplorer.loadNewer")) : loadingMore ? t("logs.loadingMore") : t("logs.loadMore")}
              </Button>
            ) : (
              <p className="text-xs text-muted-foreground">{order === "asc" ? t("logsExplorer.noNewer") : t("logs.noMore")}</p>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/** Keyboard and touch alternative to dragging: move left/right and remove. */
function ColumnMenu({ name, canLeft, canRight, onMove, onRemove }: { name: string; canLeft: boolean; canRight: boolean; onMove: (delta: -1 | 1) => void; onRemove: () => void }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const item = "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent disabled:pointer-events-none disabled:opacity-50 pointer-coarse:py-2.5";
  const run = (fn: () => void) => () => {
    fn();
    setOpen(false);
  };
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <button type="button" className="ml-auto inline-flex shrink-0 items-center rounded p-0.5 hover:bg-accent hover:text-foreground pointer-coarse:p-2" aria-label={t("logsExplorer.columns.menu", { name })} draggable={false}>
          <MoreHorizontal className="size-3.5" aria-hidden="true" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 flex w-44 flex-col gap-0.5 rounded-lg border bg-card p-1 text-card-foreground shadow-lg">
          <button type="button" className={item} disabled={!canLeft} onClick={run(() => onMove(-1))}>
            <ArrowLeft className="size-4" aria-hidden="true" />
            {t("logsExplorer.columns.moveLeft")}
          </button>
          <button type="button" className={item} disabled={!canRight} onClick={run(() => onMove(1))}>
            <ArrowRight className="size-4" aria-hidden="true" />
            {t("logsExplorer.columns.moveRight")}
          </button>
          <button type="button" className={item} onClick={run(onRemove)}>
            <Trash2 className="size-4" aria-hidden="true" />
            {t("logsExplorer.columns.remove")}
          </button>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

const KEY_STEP = 16;

function ResizeHandle({ name, width, onResize }: { name: string; width: number; onResize: (width: number, commit: boolean) => void }) {
  const { t } = useTranslation();
  const start = useRef<{ x: number; w: number; last: number } | null>(null);
  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    e.currentTarget.setPointerCapture(e.pointerId);
    start.current = { x: e.clientX, w: width, last: width };
  };
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const s = start.current;
    if (!s) return;
    s.last = s.w + e.clientX - s.x;
    onResize(s.last, false);
  };
  const onPointerUp = () => {
    if (start.current) onResize(start.current.last, true);
    start.current = null;
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    onResize(width + (e.key === "ArrowRight" ? KEY_STEP : -KEY_STEP), true);
  };
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={t("logsExplorer.columns.resize", { name })}
      aria-valuenow={width}
      aria-valuemin={MIN_COLUMN_WIDTH}
      aria-valuemax={MAX_COLUMN_WIDTH}
      tabIndex={0}
      draggable={false}
      onDragStart={(e) => e.preventDefault()}
      onClick={(e) => e.stopPropagation()}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onKeyDown={onKeyDown}
      className="absolute top-0 right-0 h-full w-2 cursor-col-resize touch-none border-r border-transparent hover:border-primary focus-visible:border-primary focus-visible:outline-none pointer-coarse:w-4"
    />
  );
}
