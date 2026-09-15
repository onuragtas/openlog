// "Top values" side panel of the explorers: the 10 most frequent values of a chosen key for the current query
// (GET /fields/values with the filters, OR groups and context) with counts and shares; click to filter in or out.
import { useQuery } from "@tanstack/react-query";
import { BarChart3, CircleMinus, CirclePlus, ListTree, X } from "lucide-react";
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { fieldValuesQuery, type ExplorerContext, type FieldSignal, type FieldType, type FilterState } from "@/api/explorer";
import { KeyPicker } from "@/components/querybuilder/KeyPicker";
import { ErrorState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { isFilterableValue, valueShare } from "@/lib/logs-explorer";
import { QB_LIMITS } from "@/lib/querybuilder";
import type { RangeSpec } from "@/lib/time";

export const TOP_VALUES_LIMIT = 10;

export interface TopValuesPanelProps {
  signal: FieldSignal;
  range: RangeSpec;
  /** Request filter of the explorer (context conditions included). */
  filter: FilterState;
  context?: ExplorerContext;
  keyName: string | undefined;
  onKeyChange: (key: string) => void;
  /** Keys offered as one-click choices before a key is picked. */
  suggestions: readonly string[];
  onFilter: (key: string, value: string, exclude: boolean, type: FieldType) => void;
  canFilter: boolean;
  onClose: () => void;
}

export function TopValuesPanel({ signal, range, filter, context, keyName, onKeyChange, suggestions, onFilter, canFilter, onClose }: TopValuesPanelProps) {
  const { t, i18n } = useTranslation();
  const id = useId();
  const locale = i18n.resolvedLanguage ?? "en";
  const key = keyName ?? "";
  const q = useQuery(fieldValuesQuery({ signal, key, range, filters: filter.filters, groups: filter.groups, context: { ...context, bodyQ: filter.q }, limit: TOP_VALUES_LIMIT }));
  const pct = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 });

  return (
    <aside aria-labelledby={`${id}-title`} className="flex min-w-0 flex-col gap-2 rounded-xl border bg-card p-3" data-testid="top-values">
      <div className="flex items-center gap-2">
        <BarChart3 className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <h2 id={`${id}-title`} className="text-sm font-medium">
          {t("explorer.topValues.title")}
        </h2>
        <Button type="button" variant="ghost" size="icon" className="ml-auto size-7" aria-label={t("explorer.topValues.close")} onClick={onClose}>
          <X aria-hidden="true" />
        </Button>
      </div>
      <div className="flex min-w-0 items-center gap-1.5">
        <span className="min-w-0 flex-1 truncate font-mono text-xs" title={key}>
          {key || <span className="font-sans text-muted-foreground">{t("explorer.topValues.noKey")}</span>}
        </span>
        <KeyPicker signal={signal} range={range} label={key ? t("explorer.topValues.changeKey") : t("explorer.topValues.pickKey")} selected={key ? [key] : []} onSelect={onKeyChange} className="h-8 shrink-0">
          <ListTree aria-hidden="true" />
          {key ? t("explorer.topValues.change") : t("explorer.topValues.pick")}
        </KeyPicker>
      </div>
      {!key ? (
        <div className="flex flex-col gap-2">
          <p className="text-xs text-muted-foreground">{t("explorer.topValues.hint")}</p>
          <div className="flex flex-wrap gap-1.5">
            {suggestions.map((k) => (
              <Button key={k} type="button" variant="outline" size="sm" className="h-7 font-mono text-xs" onClick={() => onKeyChange(k)}>
                {k}
              </Button>
            ))}
          </div>
        </div>
      ) : q.isPending ? (
        <p className="py-2 text-xs text-muted-foreground" role="status">
          {t("common.loading")}
        </p>
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} className="py-3" />
      ) : q.data.values.length === 0 ? (
        <p className="py-2 text-xs text-muted-foreground">{t("explorer.topValues.empty")}</p>
      ) : (
        <>
          <ol className="flex flex-col" aria-label={t("explorer.topValues.listLabel", { key })}>
            {q.data.values.map((v) => {
              const share = valueShare(v.count, q.data.total);
              const shown = v.value === "" ? t("queryBuilder.emptyValue") : v.value;
              const disabled = !canFilter || !isFilterableValue(v.value);
              return (
                <li key={v.value} className="flex min-w-0 flex-col gap-0.5 border-b py-1.5 last:border-0" data-testid="top-value">
                  <div className="flex min-w-0 items-center gap-1">
                    <span className="min-w-0 flex-1 truncate font-mono text-xs" title={v.value}>
                      {shown}
                    </span>
                    <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">
                      {v.count.toLocaleString(locale)} · {t("explorer.topValues.share", { value: pct.format(share) })}
                    </span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="size-7 pointer-coarse:size-9"
                      disabled={disabled}
                      title={!canFilter ? t("queryBuilder.limit", { max: QB_LIMITS.conditions }) : undefined}
                      aria-label={t("explorer.topValues.filterIn", { key, value: shown })}
                      onClick={() => onFilter(key, v.value, false, q.data.type)}
                    >
                      <CirclePlus aria-hidden="true" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="size-7 pointer-coarse:size-9"
                      disabled={disabled}
                      aria-label={t("explorer.topValues.filterOut", { key, value: shown })}
                      onClick={() => onFilter(key, v.value, true, q.data.type)}
                    >
                      <CircleMinus aria-hidden="true" />
                    </Button>
                  </div>
                  <div className="h-1 w-full overflow-hidden rounded-full bg-muted" aria-hidden="true">
                    <div className="h-full rounded-full bg-primary/70" style={{ width: `${share}%` }} />
                  </div>
                </li>
              );
            })}
          </ol>
          <p className="text-[11px] text-muted-foreground">
            {t("explorer.topValues.ofRecords", { count: q.data.total, value: q.data.total.toLocaleString(locale) })} {t("explorer.topValues.ownKeyIgnored")}
            {q.data.sampled && ` ${t("explorer.topValues.sampled")}`}
          </p>
        </>
      )}
    </aside>
  );
}
