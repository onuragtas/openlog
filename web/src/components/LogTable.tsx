// Virtualized log list (TanStack Virtual). Rows expand to show attributes;
// heights are measured. Older pages are loaded explicitly ("load more").
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

export interface LogTableProps {
  logs: LogRecord[];
  showHost?: boolean;
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  /** max height of the scrolling body */
  maxHeight?: string;
}

export function LogTable({ logs, showHost = true, hasMore, loadingMore, onLoadMore, maxHeight = "min(70vh, 56rem)" }: LogTableProps) {
  const { t, i18n } = useTranslation();
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const scrollRef = useRef<HTMLDivElement>(null);
  const locale = i18n.resolvedLanguage ?? "en";
  // Rows are only ever appended, so index + identity is a stable key.
  const keyOf = (i: number) => `${i}|${logIdentity(logs[i]!)}`;

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual is not compiler-compatible yet
  const virtualizer = useVirtualizer({
    count: logs.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 33,
    overscan: 15,
    getItemKey: keyOf,
  });

  const toggle = (k: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      return next;
    });

  return (
    <div role="table" aria-rowcount={logs.length + 1} className="text-sm">
      <div role="rowgroup" className="overflow-hidden border-b [scrollbar-gutter:stable]">
        <div role="row" className="grid items-center" style={{ gridTemplateColumns: GRID }}>
          <div role="columnheader" className="px-3">
            <span className="sr-only">{t("common.expand")}</span>
          </div>
          {(["logs.columns.time", "logs.columns.severity", "logs.columns.service", "logs.columns.body", "logs.trace"] as const).map((k) => (
            <div role="columnheader" key={k} className="h-9 content-center px-3 text-xs font-medium whitespace-nowrap text-muted-foreground">
              {t(k)}
            </div>
          ))}
        </div>
      </div>
      <div ref={scrollRef} role="rowgroup" className="overflow-auto [scrollbar-gutter:stable]" style={{ maxHeight }} data-testid="log-scroll">
        <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((vi) => {
            const l = logs[vi.index]!;
            const k = String(vi.key);
            const open = expanded.has(k);
            const ts = parseTimeParam(l.timestamp) ?? 0;
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
                <div className="grid items-center hover:bg-muted/50" style={{ gridTemplateColumns: GRID }}>
                  <div role="cell" className="px-2 py-1">
                    <button
                      type="button"
                      onClick={() => toggle(k)}
                      aria-expanded={open}
                      aria-label={open ? t("common.collapse") : t("common.expand")}
                      className="rounded p-1 hover:bg-accent"
                    >
                      {open ? <ChevronDown className="size-4" aria-hidden="true" /> : <ChevronRight className="size-4" aria-hidden="true" />}
                    </button>
                  </div>
                  <div role="cell" className="px-3 py-1 font-mono text-xs whitespace-nowrap">
                    <time dateTime={l.timestamp}>{formatDateTime(ts, locale, true)}</time>
                  </div>
                  <div role="cell" className="px-3 py-1">
                    {severityLabel(l.severity_text, l.severity_number) ? (
                      <Badge variant={severityVariant(l.severity_number)}>{severityLabel(l.severity_text, l.severity_number)}</Badge>
                    ) : (
                      <span className="text-muted-foreground" title={t("logs.severityUnspecified")}>
                        —
                      </span>
                    )}
                  </div>
                  <div role="cell" className="truncate px-3 py-1 text-xs" title={l.service_name}>
                    {l.service_name}
                  </div>
                  <div role="cell" className="min-w-0 px-3 py-1 font-mono text-xs">
                    <span className={open ? "whitespace-pre-wrap break-all" : "block truncate"}>{l.body}</span>
                  </div>
                  <div role="cell" className="px-3 py-1">
                    {l.trace_id && (
                      <Link
                        to="/traces/$traceId"
                        params={{ traceId: l.trace_id }}
                        className="font-mono text-xs text-primary underline-offset-2 hover:underline"
                        aria-label={t("logs.viewTrace", { id: l.trace_id })}
                      >
                        {l.trace_id.slice(0, 8)}
                      </Link>
                    )}
                  </div>
                </div>
                {open && (
                  <div className="grid gap-4 px-4 pb-3 pl-12 md:grid-cols-2">
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
