// Explorer table shared by the Logs and Traces Explorer: dynamic columns (any key), drag or menu reordering, resizable
// headers (pointer and keyboard), timestamp pinned first, wrap and density options, virtualized rows (TanStack Virtual)
// with "load more" paging and a per-cell value menu (filter in / out, add or remove the column, copy). Narrow containers
// render each record as a card. What a cell shows is up to the explorer (renderCell, renderCard).
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowLeft, ArrowRight, Check, CircleMinus, CirclePlus, Columns3, Copy, GripVertical, Loader2, MoreHorizontal, MoreVertical, Trash2 } from "lucide-react";
import { Popover } from "radix-ui";
import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/format";
import { isFilterableValue, MAX_COLUMN_WIDTH, MIN_COLUMN_WIDTH, moveColumn, storedWidths, storeWidths, type TablePrefs } from "@/lib/logs-explorer";
import { isCompact, useElementWidth } from "@/lib/media";
import { parseTimeParam } from "@/lib/time";
import { cn } from "@/lib/utils";

const OTHER_WIDTH = 180;
const CARD_BELOW = 640;

/** Actions of the per-cell value menu. */
export interface CellActions {
  onFilter: (key: string, value: string, exclude: boolean) => void;
  onToggleColumn: (key: string) => void;
  /** false when the condition limit is reached */
  canFilter: boolean;
}

export interface CellHelpers {
  /** Cell text (truncated or wrapped per the table preferences). */
  text: (value: string, mono?: boolean) => ReactNode;
  /** Timestamp button that opens the record details. */
  timeButton: () => ReactNode;
}

export interface DataTableProps<R extends { id: string; timestamp: string }> extends TablePrefs {
  rows: R[];
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
  ariaLabel: string;
  headerLabel: (column: string) => string;
  defaultWidths: Record<string, number>;
  /** Column that takes the remaining width until resized (e.g. the log body). */
  flexColumn?: string;
  /** localStorage key of the column widths */
  widthsKey?: string;
  /** Accessible name of the timestamp button (opens the details). */
  openLabel: (time: string) => string;
  renderCell: (row: R, column: string, index: number, helpers: CellHelpers) => ReactNode;
  renderCard: (row: R, index: number, helpers: CellHelpers) => ReactNode;
  /** Value of a cell for the cell menu (undefined: no value, no menu). */
  cellValue: (row: R, column: string) => string | undefined;
  cellActions?: CellActions;
  loadMoreLabels: { more: string; loading: string; none: string };
}

export function DataTable<R extends { id: string; timestamp: string }>({
  rows,
  columns,
  onColumnsChange,
  onOpen,
  selectedIndex,
  wrap,
  density,
  hasMore,
  loadingMore,
  onLoadMore,
  maxHeight = "min(70vh, 60rem)",
  ariaLabel,
  headerLabel,
  defaultWidths,
  flexColumn,
  widthsKey,
  openLabel,
  renderCell,
  renderCard,
  cellValue,
  cellActions,
  loadMoreLabels,
}: DataTableProps<R>) {
  const { i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const scrollRef = useRef<HTMLDivElement>(null);
  const [measureRef, width] = useElementWidth<HTMLDivElement>();
  const cards = isCompact(width, CARD_BELOW);
  const [widths, setWidths] = useState(() => storedWidths(widthsKey));
  const [drag, setDrag] = useState<{ from: number; over: number } | null>(null);
  const comfortable = density === "comfortable";

  const widthOf = (c: string) => widths[c] ?? defaultWidths[c] ?? OTHER_WIDTH;
  const template = columns.map((c) => (c === flexColumn && widths[c] === undefined ? `minmax(${widthOf(c)}px, 1fr)` : `${widthOf(c)}px`)).join(" ");
  const minWidth = columns.reduce((n, c) => n + widthOf(c), 0);

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
      if (commit) storeWidths(next, widthsKey);
      return next;
    });

  const helpers = (row: R, index: number): CellHelpers => ({
    text: (value, mono = true) => (
      <span className={cn(mono && "font-mono", "text-xs", wrap ? "break-all whitespace-pre-wrap" : "block truncate")} title={wrap ? undefined : value}>
        {value}
      </span>
    ),
    timeButton: () => {
      const time = formatDateTime(parseTimeParam(row.timestamp) ?? 0, locale, true);
      return (
        <button
          type="button"
          className="rounded-sm text-left font-mono text-xs whitespace-nowrap hover:underline pointer-coarse:py-1"
          onClick={(e) => {
            e.stopPropagation();
            onOpen(index);
          }}
          aria-label={openLabel(time)}
        >
          <time dateTime={row.timestamp}>{time}</time>
        </button>
      );
    },
  });

  return (
    <div ref={measureRef} className="text-sm">
      <div ref={scrollRef} role="table" aria-label={ariaLabel} aria-rowcount={rows.length + 1} className="overflow-auto overscroll-contain" style={{ maxHeight }} data-testid="explorer-scroll">
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
                    <span className="min-w-0 truncate">{headerLabel(c)}</span>
                    {i > 0 && (
                      <ColumnMenu
                        name={headerLabel(c)}
                        canLeft={i > 1}
                        canRight={i < columns.length - 1}
                        onMove={(d) => onColumnsChange(moveColumn(columns, i, i + d))}
                        onRemove={() => onColumnsChange(columns.filter((x) => x !== c))}
                      />
                    )}
                    <ResizeHandle name={headerLabel(c)} width={widthOf(c)} onResize={(w, commit) => resize(c, w, commit)} />
                  </div>
                ))}
              </div>
            </div>
          )}
          <div role="rowgroup" className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((vi) => {
              const row = rows[vi.index]!;
              const selected = vi.index === selectedIndex;
              const h = helpers(row, vi.index);
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
                return (
                  <div key={vi.key} {...common} className={cn("absolute top-0 left-0 w-full cursor-pointer border-b border-border/60 px-3 py-2 hover:bg-muted/50", selected && "bg-accent/60")}>
                    <div role="cell" className="flex min-w-0 flex-col gap-1">
                      {renderCard(row, vi.index, h)}
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
                  {columns.map((c) => {
                    const value = cellActions && c !== "timestamp" ? cellValue(row, c) : undefined;
                    return (
                      <div key={c} role="cell" className={cn("group/cell relative flex min-w-0 items-start gap-1 px-3", comfortable ? "py-2" : "py-1")}>
                        <div className="min-w-0 flex-1">{renderCell(row, c, vi.index, h)}</div>
                        {value !== undefined && cellActions && <CellMenu column={c} label={headerLabel(c)} value={value} actions={cellActions} isColumn />}
                      </div>
                    );
                  })}
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
                {loadingMore ? loadMoreLabels.loading : loadMoreLabels.more}
              </Button>
            ) : (
              <p className="text-xs text-muted-foreground">{loadMoreLabels.none}</p>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

const MENU_ITEM = "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent disabled:pointer-events-none disabled:opacity-50 pointer-coarse:py-2.5";

/**
 * Value menu of one cell: filter in (=), filter out (!=), add or remove the column, copy. The trigger shows on hover and
 * keyboard focus (always on touch screens); clicks never open the record details.
 */
export function CellMenu({ column, label, value, actions, isColumn }: { column: string; label: string; value: string; actions: CellActions; isColumn: boolean }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(id);
  }, [copied]);
  const filterable = actions.canFilter && isFilterableValue(value);
  const run = (fn: () => void) => () => {
    fn();
    setOpen(false);
  };
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <button
          type="button"
          onClick={(e) => e.stopPropagation()}
          className={cn(
            "inline-flex shrink-0 items-center rounded p-0.5 text-muted-foreground opacity-0 group-hover/cell:opacity-100 hover:bg-accent hover:text-foreground focus-visible:opacity-100 pointer-coarse:p-1.5 pointer-coarse:opacity-100",
            open && "opacity-100",
          )}
          aria-label={t("explorer.cell.menu", { key: label })}
        >
          <MoreVertical className="size-3.5" aria-hidden="true" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="end"
          sideOffset={4}
          collisionPadding={8}
          onClick={(e) => e.stopPropagation()}
          className="z-50 flex w-60 max-w-[calc(100vw-1rem)] flex-col gap-0.5 rounded-lg border bg-card p-1 text-card-foreground shadow-lg"
        >
          <p className="truncate px-2 py-1 font-mono text-[11px] text-muted-foreground" title={value}>
            {column} = {value === "" ? t("queryBuilder.emptyValue") : value}
          </p>
          <button type="button" className={MENU_ITEM} disabled={!filterable} onClick={run(() => actions.onFilter(column, value, false))}>
            <CirclePlus className="size-4" aria-hidden="true" />
            {t("explorer.cell.filterIn")}
          </button>
          <button type="button" className={MENU_ITEM} disabled={!filterable} onClick={run(() => actions.onFilter(column, value, true))}>
            <CircleMinus className="size-4" aria-hidden="true" />
            {t("explorer.cell.filterOut")}
          </button>
          <button type="button" className={MENU_ITEM} disabled={column === "timestamp"} onClick={run(() => actions.onToggleColumn(column))}>
            <Columns3 className="size-4" aria-hidden="true" />
            {isColumn ? t("explorer.cell.removeColumn") : t("explorer.cell.addColumn")}
          </button>
          <button type="button" className={MENU_ITEM} onClick={() => void copyText(value).then(setCopied)}>
            {copied ? <Check className="size-4" aria-hidden="true" /> : <Copy className="size-4" aria-hidden="true" />}
            {copied ? t("logsExplorer.detail.copied") : t("explorer.cell.copy")}
          </button>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** Keyboard and touch alternative to dragging: move left/right and remove. */
function ColumnMenu({ name, canLeft, canRight, onMove, onRemove }: { name: string; canLeft: boolean; canRight: boolean; onMove: (delta: -1 | 1) => void; onRemove: () => void }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
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
          <button type="button" className={MENU_ITEM} disabled={!canLeft} onClick={run(() => onMove(-1))}>
            <ArrowLeft className="size-4" aria-hidden="true" />
            {t("logsExplorer.columns.moveLeft")}
          </button>
          <button type="button" className={MENU_ITEM} disabled={!canRight} onClick={run(() => onMove(1))}>
            <ArrowRight className="size-4" aria-hidden="true" />
            {t("logsExplorer.columns.moveRight")}
          </button>
          <button type="button" className={MENU_ITEM} onClick={run(onRemove)}>
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
