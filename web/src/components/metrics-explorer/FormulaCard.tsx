// Formula over query results (`A / B * 100`), evaluated in the browser (lib/metrics-explorer.ts).
import { Sigma } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { MetricSeries } from "@/api/types";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { Input } from "@/components/ui/input";
import { evaluateFormula, FORMULA_MAX_LENGTH, metricSeriesLabel, parseFormula } from "@/lib/metrics-explorer";
import { SeriesTable } from "./SeriesTable";

export interface FormulaCardProps {
  formula: string;
  onChange: (formula: string) => void;
  ids: string[];
  /** Series per query id (undefined while loading). */
  data: Record<string, MetricSeries[] | undefined>;
  loading: boolean;
  from?: number;
  to?: number;
}

export function FormulaCard({ formula, onChange, ids, data, loading, from, to }: FormulaCardProps) {
  const { t } = useTranslation();
  const id = useId();
  const [draft, setDraft] = useState(formula);
  const parsed = useMemo(() => (formula ? parseFormula(formula, ids) : null), [formula, ids]);
  const result = useMemo(() => (parsed?.ok ? evaluateFormula(parsed.ast, parsed.refs, data) : null), [parsed, data]);
  const series = useMemo(() => result?.series.map((s) => ({ label: metricSeriesLabel(s, [], formula), points: s.points })), [result, formula]);
  const draftError = draft.trim() ? parseFormula(draft, ids) : null;

  return (
    <section aria-labelledby={`${id}-title`} className="flex min-w-0 flex-col gap-3 rounded-xl border bg-card p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Sigma className="size-4 text-muted-foreground" aria-hidden="true" />
        <h2 id={`${id}-title`} className="text-sm font-medium">
          {t("metricsExplorer.formula.title")}
        </h2>
      </div>
      <form
        className="flex flex-col gap-1.5"
        onSubmit={(e) => {
          e.preventDefault();
          onChange(draft.trim());
        }}
      >
        <label htmlFor={`${id}-input`} className="text-xs text-muted-foreground">
          {t("metricsExplorer.formula.label", { ids: ids.join(", ") })}
        </label>
        <Input
          id={`${id}-input`}
          value={draft}
          maxLength={FORMULA_MAX_LENGTH}
          placeholder={ids.length > 1 ? `${ids[0]} / ${ids[1]} * 100` : `${ids[0]} * 100`}
          aria-invalid={draftError && !draftError.ok ? true : undefined}
          aria-describedby={`${id}-help`}
          className="font-mono"
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => draft.trim() !== formula && onChange(draft.trim())}
          spellCheck={false}
          autoCapitalize="off"
          autoComplete="off"
        />
        <p id={`${id}-help`} className={draftError && !draftError.ok ? "text-xs text-destructive-text" : "text-xs text-muted-foreground"} role={draftError && !draftError.ok ? "alert" : undefined}>
          {draftError && !draftError.ok ? t(`metricsExplorer.formula.errors.${draftError.error}`, { position: draftError.position + 1, token: draftError.token ?? "" }) : t("metricsExplorer.formula.help")}
        </p>
      </form>
      {parsed?.ok && (
        <>
          {result?.unmatched ? (
            <p role="note" className="text-sm text-muted-foreground">
              {t("metricsExplorer.formula.unmatched")}
            </p>
          ) : (
            <TimeSeriesChart series={series} unit="number" from={from} to={to} height={220} title={t("metricsExplorer.formula.chart", { formula })} isLoading={loading && (series?.length ?? 0) === 0} />
          )}
          {series && series.length > 0 && <SeriesTable series={series} unit="number" caption={formula} />}
        </>
      )}
    </section>
  );
}
