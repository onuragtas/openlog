// Small APM visualizations: sparkline, latency histogram, Apdex badge and RED stat tiles.
// SVG with CSS-variable colors (theme-aware); every graphic has an accessible name.
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { apdexBadgeVariant, apdexLevel, formatApdex, formatMs, formatRate, formatRpm, shapeHistogram, type HistogramBin, type RedLike } from "@/lib/apm";
import { cn } from "@/lib/utils";

export function Sparkline({ points, label, className, width = 120, height = 28 }: { points: [number, number][]; label: string; className?: string; width?: number; height?: number }) {
  if (points.length === 0) {
    return <span className={cn("text-xs text-muted-foreground", className)}>–</span>;
  }
  const xs = points.map((p) => p[0]);
  const ys = points.map((p) => p[1]);
  const minX = Math.min(...xs);
  const spanX = Math.max(1, Math.max(...xs) - minX);
  const maxY = Math.max(...ys, 1e-9);
  const coords = points.map(([x, y]) => [((x - minX) / spanX) * (width - 2) + 1, height - 1 - (y / maxY) * (height - 2)] as const);
  const path = coords.map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
  const area = `${path} L${coords[coords.length - 1]![0].toFixed(1)},${height - 1} L${coords[0]![0].toFixed(1)},${height - 1} Z`;
  return (
    <svg role="img" aria-label={label} width={width} height={height} viewBox={`0 0 ${width} ${height}`} className={cn("overflow-visible", className)} data-testid="sparkline">
      <path d={area} className="fill-primary/15" />
      <path d={path} className="fill-none stroke-primary" strokeWidth={1.5} strokeLinejoin="round" />
    </svg>
  );
}

export function ApdexBadge({ apdex, tMs, className }: { apdex: number | null | undefined; tMs?: number; className?: string }) {
  const { t, i18n } = useTranslation();
  const level = apdexLevel(apdex);
  const locale = i18n.resolvedLanguage ?? "en";
  const title = `${t(`apm.apdexLevels.${level}`)}${tMs ? ` · ${t("apm.apdexT", { value: formatMs(tMs, locale) })}` : ""}`;
  return (
    <Badge variant={apdexBadgeVariant(level)} className={cn("font-mono tabular-nums", className)} title={title}>
      {formatApdex(apdex, locale)}
      <span className="sr-only">{title}</span>
    </Badge>
  );
}

export function LatencyHistogram({ bins, height = 160 }: { bins: HistogramBin[]; height?: number }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const shaped = shapeHistogram(bins);
  if (shaped.length === 0) return <p className="py-6 text-center text-sm text-muted-foreground">{t("apm.transactions.noTraces")}</p>;
  const max = Math.max(...shaped.map((b) => b.count), 1e-9);
  return (
    <figure className="flex flex-col gap-1" data-testid="latency-histogram">
      <figcaption className="sr-only">{t("apm.transactions.histogram")}</figcaption>
      <ul className="flex items-end gap-px" style={{ height }} aria-label={t("apm.transactions.histogram")}>
        {shaped.map((b, i) => {
          const label = t("apm.transactions.histogramBar", {
            count: Math.round(b.count),
            from: formatMs(b.from_ms, locale),
            to: formatMs(b.to_ms, locale),
          });
          return (
            <li key={i} className="flex h-full min-w-1 flex-1 items-end" title={label}>
              <span className="sr-only">{label}</span>
              <span aria-hidden="true" className="block w-full rounded-t-sm bg-primary/70" style={{ height: `${b.count > 0 ? Math.max(2, (b.count / max) * 100) : 0}%` }} />
            </li>
          );
        })}
      </ul>
      <div className="flex justify-between font-mono text-[11px] text-muted-foreground" aria-hidden="true">
        <span>{formatMs(shaped[0]!.from_ms, locale)}</span>
        <span>{formatMs(shaped[Math.floor(shaped.length / 2)]!.to_ms, locale)}</span>
        <span>{formatMs(shaped[shaped.length - 1]!.to_ms, locale)}</span>
      </div>
    </figure>
  );
}

/** Summary tiles: throughput, error rate, p50/p95/p99, Apdex. */
export function RedTiles({ red, apdexTMs }: { red: RedLike; apdexTMs?: number }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const tiles: [string, React.ReactNode][] = [
    [t("apm.metrics.throughput"), formatRpm(red.throughput, locale)],
    [t("apm.metrics.errorRate"), formatRate(red.error_rate, locale)],
    [t("apm.metrics.avg"), formatMs(red.avg_ms, locale)],
    [t("apm.metrics.p95"), formatMs(red.p95_ms, locale)],
    [t("apm.metrics.p99"), formatMs(red.p99_ms, locale)],
    [t("apm.metrics.apdex"), <ApdexBadge key="apdex" apdex={red.apdex} tMs={apdexTMs} className="text-sm" />],
  ];
  return (
    <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6" data-testid="red-tiles">
      {tiles.map(([label, value]) => (
        <div key={label} className="rounded-lg border bg-card px-3 py-2">
          <dt className="text-xs text-muted-foreground">{label}</dt>
          <dd className="mt-0.5 font-mono text-base font-semibold tabular-nums">{value}</dd>
        </div>
      ))}
    </dl>
  );
}
