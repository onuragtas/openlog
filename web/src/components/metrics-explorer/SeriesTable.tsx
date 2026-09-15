import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { formatValue, type UnitKind } from "@/lib/format";
import { seriesStats } from "@/lib/metrics-explorer";

const COLLAPSED = 10;

/** Series of a chart with their last, average, minimum and maximum values. */
export function SeriesTable({ series, unit, caption }: { series: { label: string; points: [number, number][] }[]; unit: UnitKind; caption: string }) {
  const { t, i18n } = useTranslation();
  const [all, setAll] = useState(false);
  const locale = i18n.resolvedLanguage ?? "en";
  const shown = all ? series : series.slice(0, COLLAPSED);
  const fmt = (v: number | null) => formatValue(v, unit, locale);
  return (
    <div className="flex flex-col gap-1">
      <div className="overflow-x-auto">
        <table className="w-full text-xs">
          <caption className="sr-only">{t("metricsExplorer.seriesTable", { name: caption })}</caption>
          <thead>
            <tr className="border-b text-left text-muted-foreground">
              <th scope="col" className="py-1.5 pr-3 font-medium">
                {t("metricsExplorer.columns.series")}
              </th>
              {(["last", "avg", "min", "max"] as const).map((k) => (
                <th key={k} scope="col" className="py-1.5 pl-3 text-right font-medium whitespace-nowrap">
                  {t(`metricsExplorer.columns.${k}`)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.map((s, i) => {
              const st = seriesStats(s.points);
              return (
                <tr key={`${i}|${s.label}`} className="border-b last:border-0">
                  <th scope="row" className="max-w-[24rem] truncate py-1 pr-3 text-left font-mono font-normal" title={s.label}>
                    {s.label}
                  </th>
                  <td className="py-1 pl-3 text-right font-mono whitespace-nowrap tabular-nums">{fmt(st.last)}</td>
                  <td className="py-1 pl-3 text-right font-mono whitespace-nowrap tabular-nums">{fmt(st.avg)}</td>
                  <td className="py-1 pl-3 text-right font-mono whitespace-nowrap tabular-nums">{fmt(st.min)}</td>
                  <td className="py-1 pl-3 text-right font-mono whitespace-nowrap tabular-nums">{fmt(st.max)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {series.length > COLLAPSED && (
        <Button type="button" variant="link" size="sm" className="self-start px-0" aria-expanded={all} onClick={() => setAll((a) => !a)}>
          {all ? t("charts.legendLess") : t("charts.legendMore", { count: series.length - COLLAPSED })}
        </Button>
      )}
    </div>
  );
}
