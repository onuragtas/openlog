import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { alertEvaluationsQuery, type AlertRule } from "@/api/alerts";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { NativeSelect } from "@/components/ui/native-select";
import { historyChartData } from "@/lib/alert-history";
import type { UnitKind } from "@/lib/format";
import { Section } from "./fields";

const PreviewChart = lazy(() => import("./PreviewChart").then((m) => ({ default: m.PreviewChart })));

const RANGES = [1, 6, 24, 168] as const;

const parse = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));

function unitFor(rule: AlertRule): UnitKind {
  const metric = rule.condition.metric ?? "";
  if (rule.type === "apm" && metric.endsWith("_ms")) return "ms";
  if (rule.type === "apm" && metric === "error_rate") return "percent";
  return "number";
}

/** Evaluation history of a saved rule (GET /alerts/rules/{id}/evaluations, alerting.md §3.6). */
export function EvaluationHistory({ rule }: { rule: AlertRule }) {
  const { t } = useTranslation();
  const uid = useId();
  const [hours, setHours] = useState<number>(24);
  const q = useQuery(alertEvaluationsQuery(rule.id, hours));
  const data = useMemo(() => (q.data ? historyChartData(q.data, t("charts.series")) : null), [q.data, t]);
  const threshold = typeof rule.condition.threshold === "number" ? rule.condition.threshold : null;
  const recovery = typeof rule.condition.recovery_threshold === "number" ? rule.condition.recovery_threshold : null;
  return (
    <Section title={t("alerts.history.title")}>
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor={`${uid}-range`} className="text-sm font-medium">
          {t("alerts.history.range")}
        </label>
        <NativeSelect id={`${uid}-range`} value={hours} onChange={(e) => setHours(Number(e.target.value))}>
          {RANGES.map((h) => (
            <option key={h} value={h}>
              {t("alerts.preview.hoursOption", { count: h })}
            </option>
          ))}
        </NativeSelect>
      </div>
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} className="py-6" />
      ) : !data || data.evaluations === 0 ? (
        <EmptyState className="py-6">{t("alerts.history.empty")}</EmptyState>
      ) : (
        <div className="flex flex-col gap-2" data-testid="alert-evaluation-history">
          <p role="status" className="text-sm text-muted-foreground">
            {t("alerts.history.summary", { count: data.evaluations, errors: data.errors, ms: data.lastDurationMs ?? 0 })}
          </p>
          {data.series.length > 0 && (
            <Suspense fallback={<LoadingState />}>
              <PreviewChart
                series={data.series}
                unit={unitFor(rule)}
                threshold={threshold}
                recoveryThreshold={recovery}
                bands={data.bands}
                fires={[]}
                from={parse(q.data.from)}
                to={parse(q.data.to)}
                title={t("alerts.history.title")}
              />
            </Suspense>
          )}
          {(data.hidden > 0 || q.data.truncated) && <p className="text-xs text-muted-foreground">{t("alerts.history.truncated")}</p>}
        </div>
      )}
    </Section>
  );
}
