import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Gauge } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { usageStatusQuery, worstMetric } from "@/api/usage";
import { cn } from "@/lib/utils";

/** Shown when the organization is near or over a plan limit, or ingest is blocked (GET /api/v1/usage/status). */
export function UsageBanner() {
  const { t } = useTranslation();
  const me = useMe().data;
  const status = useQuery({ ...usageStatusQuery(), enabled: !!me?.organization }).data;
  if (!status || (status.level === "ok" && !status.ingest_blocked)) return null;

  const worst = worstMetric(status.metrics);
  const exceeded = status.ingest_blocked || status.level === "exceeded";
  let message: string;
  if (status.ingest_blocked) {
    message = t("usage.banner.blocked");
  } else if (worst) {
    const metric = t(`usage.metrics.${worst.metric}`);
    message = t(worst.level === "exceeded" ? "usage.banner.exceeded" : "usage.banner.warning", { metric, percent: Math.round(worst.percent) });
  } else {
    return null;
  }
  return (
    <div
      role="status"
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-2 text-sm",
        exceeded ? "border-destructive/60 bg-destructive/10" : "border-warning/60 bg-warning/10",
      )}
    >
      <Gauge className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1 break-words">{message}</span>
      <Link to="/settings/usage" className="font-medium underline underline-offset-4">
        {t("usage.banner.details")}
      </Link>
    </div>
  );
}
