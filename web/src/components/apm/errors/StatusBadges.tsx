import { useTranslation } from "react-i18next";
import type { ApmErrorStatus } from "@/api/apm";
import { Badge } from "@/components/ui/badge";
import { isRegressed } from "@/lib/apm-errors";

const VARIANT: Record<ApmErrorStatus, "outline" | "success" | "muted"> = { unresolved: "outline", resolved: "success", ignored: "muted" };

/** Status badge plus a "Regressed" badge for groups that reopened automatically. */
export function StatusBadges({ group }: { group: { status: ApmErrorStatus; regressed_at: string | null; regression_count: number } }) {
  const { t } = useTranslation();
  return (
    <span className="inline-flex flex-wrap gap-1">
      <Badge variant={VARIANT[group.status] ?? "outline"}>{t(`apm.errors.statuses.${group.status}`)}</Badge>
      {isRegressed(group) && (
        <Badge variant="warning" title={t("apm.errors.regressedTitle")}>
          {t("apm.errors.regressed")}
        </Badge>
      )}
    </span>
  );
}
