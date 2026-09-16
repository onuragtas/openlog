// Exemplars of a Metrics Explorer chart (POST /api/v1/metrics/exemplars, D-130): the traces recorded while the metric
// was measured. TimeSeriesChart draws them as dots so a spike is visibly clickable; this list is the part that can
// actually be clicked and reached with a keyboard, one row per dot, each linking to its trace and span.
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { MetricExemplar } from "@/api/explorer";
import { Button } from "@/components/ui/button";
import { formatDateTime, formatValue, type UnitKind } from "@/lib/format";

const COLLAPSED = 10;

export interface ExemplarsPanelProps {
  exemplars: readonly MetricExemplar[];
  /** Matching exemplars in the range, not only the listed ones. */
  total: number;
  truncated: boolean;
  unit: UnitKind;
  /** Same factor the chart's series were scaled by, so a listed value matches its dot. */
  scale: number;
}

export function ExemplarsPanel({ exemplars, total, truncated, unit, scale }: ExemplarsPanelProps) {
  const { t, i18n } = useTranslation();
  const [all, setAll] = useState(false);
  const locale = i18n.resolvedLanguage ?? "en";
  const shown = all ? exemplars : exemplars.slice(0, COLLAPSED);

  return (
    <section className="flex min-w-0 flex-col gap-1" data-testid="metric-exemplars">
      <h3 className="text-xs font-medium">{t("metricsExplorer.exemplars.title")}</h3>
      <p className="text-[11px] text-muted-foreground">{t("metricsExplorer.exemplars.description")}</p>
      <div className="overflow-x-auto">
        <table className="w-full text-xs">
          <caption className="sr-only">{t("metricsExplorer.exemplars.title")}</caption>
          <thead>
            <tr className="border-b text-left text-muted-foreground">
              <th scope="col" className="py-1.5 pr-3 font-medium">
                {t("metricsExplorer.exemplars.columns.time")}
              </th>
              <th scope="col" className="py-1.5 pl-3 text-right font-medium whitespace-nowrap">
                {t("metricsExplorer.exemplars.columns.value")}
              </th>
              <th scope="col" className="py-1.5 pl-3 font-medium">
                {t("metricsExplorer.exemplars.columns.service")}
              </th>
              <th scope="col" className="py-1.5 pl-3 font-medium">
                {t("metricsExplorer.exemplars.columns.trace")}
              </th>
            </tr>
          </thead>
          <tbody>
            {shown.map((e) => (
              <tr key={`${e.timestamp}|${e.trace_id}`} className="border-b last:border-0" data-testid="metric-exemplar">
                <th scope="row" className="py-1 pr-3 text-left font-normal whitespace-nowrap">
                  {formatDateTime(Date.parse(e.timestamp), locale, true)}
                </th>
                <td className="py-1 pl-3 text-right font-mono whitespace-nowrap tabular-nums">{formatValue(e.value * scale, unit, locale)}</td>
                <td className="max-w-[12rem] truncate py-1 pl-3" title={e.service_name}>
                  {e.service_name}
                </td>
                <td className="py-1 pl-3">
                  <Link
                    to="/traces/$traceId"
                    params={{ traceId: e.trace_id }}
                    search={e.span_id ? { span: e.span_id } : {}}
                    className="font-mono text-xs text-primary hover:underline"
                    aria-label={t("metricsExplorer.exemplars.open", { id: e.trace_id })}
                  >
                    {e.trace_id.slice(0, 12)}…
                  </Link>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {exemplars.length > COLLAPSED && (
        <Button type="button" variant="link" size="sm" className="self-start px-0" aria-expanded={all} onClick={() => setAll((a) => !a)}>
          {all ? t("charts.legendLess") : t("charts.legendMore", { count: exemplars.length - COLLAPSED })}
        </Button>
      )}
      {truncated && (
        <p role="note" className="text-[11px] text-muted-foreground">
          {t("metricsExplorer.exemplars.truncated", { count: exemplars.length, total })}
        </p>
      )}
    </section>
  );
}
