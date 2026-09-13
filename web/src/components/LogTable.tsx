// Virtualized log list (TanStack Virtual). Rows expand to show attributes;
// heights are measured. Older pages are loaded explicitly ("load more").
// In narrow containers (phones, tablets without room for all columns) each
// record becomes a card: time, severity, service and trace on one line, the body below.
import { Link } from "@tanstack/react-router";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ChevronDown, ChevronRight, Loader2 } from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { LogRecord } from "@/api/types";
import { AttributeTable } from "@/components/JsonView";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatDateTime } from "@/lib/format";
import { isCompact, useElementWidth } from "@/lib/media";
import { severityLabel } from "@/lib/severity";
import { logIdentity } from "@/lib/logs";
import { parseTimeParam } from "@/lib/time";
import { cn } from "@/lib/utils";

function severityVariant(n: number): "destructive" | "warning" | "secondary" | "muted" {
  if (n >= 17) return "destructive";
  if (n >= 13) return "warning";
  if (n >= 9) return "secondary";
  return "muted";
}

const GRID = "2.5rem 13.5rem 5.5rem minmax(5rem,9rem) minmax(12rem,1fr) 5.5rem";
const COMPACT_GRID = "2.75rem minmax(0,1fr)";
/** The full grid needs ~46rem (plus the scrollbar gutter). */
const COMPACT_BELOW = 760;

export interface LogTableProps {
  logs: LogRecord[];
  showHost?: boolean;
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  /** max height of the scrolling body */
  maxHeight?: string;
}

function Severity({ log }: { log: LogRecord }) {
  const { t } = useTranslation();
  const label = severityLabel(log.severity_text, log.severity_number);
  return label ? (
    <Badge variant={severityVariant(log.severity_number)}>{label}</Badge>
  ) : (
    <span className="text-muted-foreground" title={t("logs.severityUnspecified")}>
      —
    </span>
  );
}

function TraceLink({ traceId }: { traceId: string }) {
  const { t } = useTranslation();
  return (
    <Link
      to="/traces/$traceId"
      params={{ traceId }}
      className="font-mono text-xs text-primary underline-offset-2 hover:underline"
      aria-label={t("logs.viewTrace", { id: traceId })}
    >
      {traceId.slice(0, 8)}
    </Link>
  );
}

export function LogTable({ logs, showHost = true, hasMore, loadingMore, onLoadMore, maxHeight = "min(70vh, 56rem)" }: LogTableProps) {
  const { t, i18n } = useTranslation();
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const scrollRef = useRef<HTMLDivElement>(null);
  const [measureRef, width] = useElementWidth<HTMLDivElement>();
  const compact = isCompact(width, COMPACT_BELOW);
  const locale = i18n.resolvedLanguage ?? "en";
  // Rows are only ever appended, so index + identity is a stable key.
  const keyOf = (i: number) => `${i}|${logIdentity(logs[i]!)}`;

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual is not compiler-compatible yet
  const virtualizer = useVirtualizer({
    count: logs.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => (compact ? 64 : 33),
    overscan: 15,
    // The layout is part of the key: switching remounts rows so their new heights are measured.
    getItemKey: (i) => `${compact ? "c" : "w"}|${keyOf(i)}`,
  });

  const toggle = (k: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      return next;
    });

  return (
    <div ref={measureRef} role="table" aria-rowcount={logs.length + 1} className="text-sm">
      <div role="rowgroup" className="overflow-hidden border-b [scrollbar-gutter:stable]">
        <div role="row" className="grid items-center" style={{ gridTemplateColumns: compact ? COMPACT_GRID : GRID }}>
          <div role="columnheader" className="px-3">
            <span className="sr-only">{t("common.expand")}</span>
          </div>
          {compact ? (
            <div role="columnheader" className="h-9 content-center pr-3 text-xs font-medium whitespace-nowrap text-muted-foreground">
              {t("logs.columns.body")}
            </div>
          ) : (
            (["logs.columns.time", "logs.columns.severity", "logs.columns.service", "logs.columns.body", "logs.trace"] as const).map((k) => (
              <div role="columnheader" key={k} className="h-9 content-center px-3 text-xs font-medium whitespace-nowrap text-muted-foreground">
                {t(k)}
              </div>
            ))
          )}
        </div>
      </div>
      <div ref={scrollRef} role="rowgroup" className="overflow-auto overscroll-contain [scrollbar-gutter:stable]" style={{ maxHeight }} data-testid="log-scroll">
        <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((vi) => {
            const l = logs[vi.index]!;
            const k = keyOf(vi.index);
            const open = expanded.has(k);
            const ts = parseTimeParam(l.timestamp) ?? 0;
            const expandButton = (
              <button
                type="button"
                onClick={() => toggle(k)}
                aria-expanded={open}
                aria-label={open ? t("common.collapse") : t("common.expand")}
                className="rounded p-1 hover:bg-accent pointer-coarse:p-2.5"
              >
                {open ? <ChevronDown className="size-4" aria-hidden="true" /> : <ChevronRight className="size-4" aria-hidden="true" />}
              </button>
            );
            return (
              <div
                key={vi.key}
                data-index={vi.index}
                ref={virtualizer.measureElement}
                role="row"
                aria-rowindex={vi.index + 2}
                className={cn("absolute top-0 left-0 w-full border-b border-border/60", open && "bg-muted/40")}
                style={{ transform: `translateY(${vi.start}px)` }}
              >
                {compact ? (
                  <div className="grid items-start hover:bg-muted/50" style={{ gridTemplateColumns: COMPACT_GRID }}>
                    <div role="cell" className="px-1 py-1">
                      {expandButton}
                    </div>
                    <div role="cell" className="flex min-w-0 flex-col gap-1 py-2 pr-3">
                      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                        <time dateTime={l.timestamp} className="font-mono">
                          {formatDateTime(ts, locale, true)}
                        </time>
                        <Severity log={l} />
                        {l.service_name && (
                          <span className="max-w-full truncate text-muted-foreground" title={l.service_name}>
                            {l.service_name}
                          </span>
                        )}
                        {l.trace_id && <TraceLink traceId={l.trace_id} />}
                      </div>
                      <span className={cn("font-mono text-xs break-all", open ? "whitespace-pre-wrap" : "line-clamp-2")}>{l.body}</span>
                    </div>
                  </div>
                ) : (
                  <div className="grid items-center hover:bg-muted/50" style={{ gridTemplateColumns: GRID }}>
                    <div role="cell" className="px-2 py-1">
                      {expandButton}
                    </div>
                    <div role="cell" className="px-3 py-1 font-mono text-xs whitespace-nowrap">
                      <time dateTime={l.timestamp}>{formatDateTime(ts, locale, true)}</time>
                    </div>
                    <div role="cell" className="px-3 py-1">
                      <Severity log={l} />
                    </div>
                    <div role="cell" className="truncate px-3 py-1 text-xs" title={l.service_name}>
                      {l.service_name}
                    </div>
                    <div role="cell" className="min-w-0 px-3 py-1 font-mono text-xs">
                      <span className={open ? "whitespace-pre-wrap break-all" : "block truncate"}>{l.body}</span>
                    </div>
                    <div role="cell" className="px-3 py-1">
                      {l.trace_id && <TraceLink traceId={l.trace_id} />}
                    </div>
                  </div>
                )}
                {open && (
                  <div className={cn("grid gap-4 pb-3 md:grid-cols-2", compact ? "px-3" : "px-4 pl-12")}>
                    <AttributeTable
                      caption={t("logs.attributes")}
                      attributes={{ ...l.attributes, ...(showHost && l.host_id ? { host_id: l.host_id } : {}), ...(l.span_id ? { span_id: l.span_id } : {}) }}
                    />
                    <AttributeTable caption={t("logs.resource")} attributes={l.resource_attributes} />
                  </div>
                )}
              </div>
            );
          })}
        </div>
        {onLoadMore && (
          <div className="flex justify-center border-t p-3">
            {hasMore ? (
              <Button variant="outline" size="sm" onClick={onLoadMore} disabled={loadingMore} aria-live="polite">
                {loadingMore && <Loader2 className="animate-spin" aria-hidden="true" />}
                {loadingMore ? t("logs.loadingMore") : t("logs.loadMore")}
              </Button>
            ) : (
              <p className="text-xs text-muted-foreground">{t("logs.noMore")}</p>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
