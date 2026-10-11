// One Core Web Vital, as a developer reads it: the p75 and its verdict, how far that is from the threshold
// that would change the verdict, where the rest of the distribution sits, and how many measurements are
// behind it all.
//
// The p75 leads because that is the percentile the Core Web Vitals assessment is defined on. The thresholds
// come from the server with the vital (rum.md §2.1) — they are constants of the programme, not settings of
// this installation — so nothing here recomputes a rating; it only says what the numbers are.
import { useTranslation } from "react-i18next";
import type { RumVital } from "@/api/rum";
import { Badge } from "@/components/ui/badge";
import { formatShare, formatVital, ratingKey, ratingVariant } from "@/lib/rum";

/** The three bands are shares of the same total, so they read as one bar rather than three numbers. */
function Bands({ vital }: { vital: RumVital }) {
  const { t } = useTranslation();
  const bands = [
    { key: "good", share: vital.good, className: "bg-success" },
    { key: "needs_improvement", share: vital.needs_improvement, className: "bg-warning" },
    { key: "poor", share: vital.poor, className: "bg-destructive" },
  ];
  return (
    <div className="flex flex-col gap-1">
      <div className="flex h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
        {bands.map((b) => (b.share > 0 ? <div key={b.key} className={b.className} style={{ width: `${b.share * 100}%` }} /> : null))}
      </div>
      <p className="text-xs text-muted-foreground">
        {t("rum.vitals.bands", { good: formatShare(vital.good), needsImprovement: formatShare(vital.needs_improvement), poor: formatShare(vital.poor) })}
      </p>
    </div>
  );
}

/**
 * The two published boundaries, and how far the p75 is from the one that matters.
 *
 * "LCP is 3.2 s" is half an answer; the other half is "good ends at 2.5 s, so it is 0.7 s over". A vital
 * already rated good is measured against the boundary it would lose, which is the same sentence read the
 * other way round: how much headroom is left.
 */
function Thresholds({ vital }: { vital: RumVital }) {
  const { t, i18n } = useTranslation();
  const good = formatVital(vital.good_threshold, vital.name, i18n.language);
  const poor = formatVital(vital.poor_threshold, vital.name, i18n.language);
  const p75 = vital.p75;
  let distance: string | null = null;
  if (p75 !== null && vital.rating !== "") {
    // Good is measured against its own ceiling; anything worse against the ceiling it has to get back under.
    const target = vital.rating === "poor" ? vital.poor_threshold : vital.good_threshold;
    const amount = formatVital(Math.abs(p75 - target), vital.name, i18n.language);
    const threshold = vital.rating === "poor" ? poor : good;
    distance = vital.rating === "good" ? t("rum.vitals.headroom", { amount, threshold }) : t("rum.vitals.over", { amount, threshold });
  }
  return (
    <div className="flex flex-col gap-0.5 text-xs text-muted-foreground">
      <p>{t("rum.vitals.thresholds", { good, poor })}</p>
      {distance && <p className="text-foreground">{distance}</p>}
    </div>
  );
}

/** p50/p75/p95: one number is a verdict, three are a shape. A long tail shows up here and nowhere else. */
function Percentiles({ vital }: { vital: RumVital }) {
  const { t, i18n } = useTranslation();
  // The literal union, not string: t() takes a known key, and a widened `${string}` is not one.
  const cells: ["p50" | "p75" | "p95", number | null][] = [
    ["p50", vital.p50],
    ["p75", vital.p75],
    ["p95", vital.p95],
  ];
  return (
    <dl className="grid grid-cols-3 gap-1 text-xs">
      {cells.map(([label, value]) => (
        <div key={label}>
          <dt className="text-muted-foreground">{t(`rum.vitals.percentiles.${label}`)}</dt>
          <dd className="tabular-nums">{formatVital(value, vital.name, i18n.language)}</dd>
        </div>
      ))}
    </dl>
  );
}

export function VitalCard({ vital }: { vital: RumVital }) {
  const { t, i18n } = useTranslation();
  const measured = vital.count > 0;
  return (
    <div className="flex flex-col gap-3 rounded-xl border bg-card p-3" data-testid="rum-vital">
      <div>
        <div className="flex items-center justify-between gap-2">
          <p className="text-xs font-medium uppercase">{t(`rum.vitals.names.${vital.name}`)}</p>
          <Badge variant={ratingVariant(vital.rating)}>{t(ratingKey(vital.rating))}</Badge>
        </div>
        <p className="mt-1 text-xl font-semibold tabular-nums">{formatVital(vital.p75, vital.name, i18n.language)}</p>
        <p className="text-xs text-muted-foreground">{measured ? t("rum.vitals.count", { n: Math.round(vital.count) }) : t("rum.vitals.noData")}</p>
      </div>
      {measured && (
        <>
          <Percentiles vital={vital} />
          <Bands vital={vital} />
          <Thresholds vital={vital} />
        </>
      )}
    </div>
  );
}
