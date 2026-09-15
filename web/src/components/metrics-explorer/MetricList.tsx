// Searchable list of every metric in the range (GET /api/v1/metrics) with its metadata.
import { useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { metricsListQuery, type MetricInfo } from "@/api/explorer";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import type { RangeSpec } from "@/lib/time";
import { parseTimeParam } from "@/lib/time";
import { useDebounced } from "@/lib/use-debounced";
import { cn } from "@/lib/utils";

const SHOWN = 300;

export function MetricList({ range, selected, target, onSelect, className }: { range: RangeSpec; selected?: string; target: string; onSelect: (name: string) => void; className?: string }) {
  const { t } = useTranslation();
  const id = useId();
  const [text, setText] = useState("");
  const q = useDebounced(text.trim(), 250);
  const list = useQuery(metricsListQuery({ range, q }));
  const now = useNow();
  const lower = text.trim().toLowerCase();
  const metrics = (list.data?.metrics ?? []).filter((m) => m.name.toLowerCase().includes(lower));

  return (
    <section aria-labelledby={`${id}-title`} className={cn("flex min-h-0 flex-col gap-2 rounded-xl border bg-card p-3", className)}>
      <div className="flex items-baseline justify-between gap-2">
        <h2 id={`${id}-title`} className="text-sm font-medium">
          {t("metricsExplorer.list.title")}
        </h2>
        <span className="text-xs text-muted-foreground">{t("metricsExplorer.list.target", { id: target })}</span>
      </div>
      <label htmlFor={`${id}-q`} className="sr-only">
        {t("metricsExplorer.list.search")}
      </label>
      <Input id={`${id}-q`} type="search" value={text} placeholder={t("metricsExplorer.list.search")} onChange={(e) => setText(e.target.value)} className="font-mono" />
      {list.isPending ? (
        <LoadingState />
      ) : list.isError && !list.data ? (
        <ErrorState error={list.error} onRetry={() => void list.refetch()} />
      ) : metrics.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{lower ? t("metricsExplorer.list.noMatch") : t("metricsExplorer.list.empty")}</p>
      ) : (
        <ul className="-mx-1 flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto overscroll-contain" aria-label={t("metricsExplorer.list.title")}>
          {metrics.slice(0, SHOWN).map((m) => (
            <MetricItem key={m.name} metric={m} now={now} selected={m.name === selected} onSelect={() => onSelect(m.name)} />
          ))}
        </ul>
      )}
      {(metrics.length > SHOWN || list.data?.truncated) && <p className="text-xs text-muted-foreground">{t("metricsExplorer.list.refine")}</p>}
    </section>
  );
}

function MetricItem({ metric: m, now, selected, onSelect }: { metric: MetricInfo; now: number; selected: boolean; onSelect: () => void }) {
  const { t, i18n } = useTranslation();
  const lastSeen = parseTimeParam(m.last_seen);
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        className={cn("flex w-full flex-col gap-1 rounded-md px-2 py-2 text-left hover:bg-accent", selected && "bg-accent")}
      >
        <span className="font-mono text-xs font-medium break-all">{m.name}</span>
        <span className="flex flex-wrap items-center gap-1">
          <Badge variant="secondary">{m.type || t("common.unknown")}</Badge>
          {m.unit && <Badge variant="outline" className="font-mono">{m.unit}</Badge>}
          {m.type === "sum" && <Badge variant="muted">{m.monotonic ? t("metricsExplorer.monotonic") : t("metricsExplorer.nonMonotonic")}</Badge>}
          {m.temporality !== "unspecified" && <Badge variant="muted">{t(`metricsExplorer.temporality.${m.temporality}`)}</Badge>}
        </span>
        {m.description && <span className="line-clamp-2 text-xs text-muted-foreground">{m.description}</span>}
        <span className="flex flex-wrap gap-x-3 text-[11px] text-muted-foreground">
          <span>{t("metricsExplorer.series", { count: m.series })}</span>
          {m.services.length > 0 && <span className="truncate">{m.services.join(", ")}</span>}
          {lastSeen !== null && <span>{t("metricsExplorer.lastSeen", { time: formatRelative(lastSeen, now, i18n.resolvedLanguage ?? "en") })}</span>}
        </span>
      </button>
    </li>
  );
}
