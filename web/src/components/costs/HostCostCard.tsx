import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Wallet } from "lucide-react";
import { useTranslation } from "react-i18next";
import { costHostQuery, formatMoney, formatShare } from "@/api/costs";
import type { RangeSpec } from "@/lib/time";

/**
 * "What this host costs" on the host detail page (cost.md). An estimate from a static price table:
 * the price table's caveat is one click away in the Costs view, and a host openlog cannot price
 * says so rather than showing a zero.
 */
export function HostCostCard({ hostId, range }: { hostId: string; range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(costHostQuery(hostId, range));
  // Cost estimates are optional (OPENLOG_COST_ENABLED): no answer, no card.
  if (!q.data?.host) return null;
  const h = q.data.host;
  const currency = q.data.pricing?.currency;

  return (
    <section aria-labelledby="host-cost" className="mt-3" data-testid="host-cost">
      <h2 id="host-cost" className="mb-1 text-xs font-semibold text-muted-foreground">
        {t("costs.host.title")}
      </h2>
      {h.priced ? (
        <div className="flex flex-wrap items-center gap-2">
          <Link
            to="/costs"
            search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}
            className="inline-flex items-center gap-1.5 rounded-md border bg-card px-2 py-1 text-xs hover:border-primary"
            aria-label={t("costs.host.open")}
          >
            <Wallet className="size-3 text-muted-foreground" aria-hidden="true" />
            <span className="font-medium">{formatMoney(h.total, currency, locale)}</span>
            <span className="text-muted-foreground">{t("costs.host.perHour", { value: formatMoney(h.price.usd_per_hour, currency, locale) })}</span>
          </Link>
          <span className="rounded-md border bg-card px-2 py-1 text-xs text-muted-foreground">
            {t("costs.host.idleShare", { percent: formatShare(h.idle_share, locale) })}
          </span>
          {h.instance_type && (
            <span className="rounded-md border bg-card px-2 py-1 font-mono text-xs text-muted-foreground">
              {h.instance_type}
              {h.region ? ` · ${h.region}` : ""}
              {h.lifecycle && h.lifecycle !== "on-demand" ? ` · ${h.lifecycle}` : ""}
            </span>
          )}
          {h.price.source === "fallback" && <span className="rounded-md border bg-card px-2 py-1 text-xs text-muted-foreground">{t("costs.source.fallback")}</span>}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t("costs.host.notPriced")}</p>
      )}
    </section>
  );
}
