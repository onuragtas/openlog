import { useTranslation } from "react-i18next";
import type { AlertDelivery } from "@/api/alerts";
import { DateTimeText } from "@/components/settings/common";
import { EmptyState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ChannelTypeIcon } from "./badges";

export function DeliveriesTable({ deliveries }: { deliveries: AlertDelivery[] }) {
  const { t } = useTranslation();
  if (deliveries.length === 0) return <EmptyState className="py-4">{t("alerts.deliveries.empty")}</EmptyState>;
  return (
    <Table mobile="stack">
      <TableHeader>
        <TableRow>
          <TableHead>{t("alerts.deliveries.columns.channel")}</TableHead>
          <TableHead>{t("alerts.deliveries.columns.kind")}</TableHead>
          <TableHead>{t("alerts.deliveries.columns.status")}</TableHead>
          <TableHead>{t("alerts.deliveries.columns.attempts")}</TableHead>
          <TableHead>{t("alerts.deliveries.columns.created")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {deliveries.map((d) => (
          <TableRow key={d.id} data-testid="delivery-row">
            <TableCell>
              <span className="inline-flex items-center gap-2">
                <ChannelTypeIcon type={d.channel_type} />
                {d.channel_name || d.channel_type}
              </span>
            </TableCell>
            <TableCell label={t("alerts.deliveries.columns.kind")}>{t(`alerts.deliveries.kinds.${d.kind}`)}</TableCell>
            <TableCell>
              <Badge variant={d.status === "delivered" ? "success" : d.status === "failed" ? "destructive" : d.status === "suppressed" ? "muted" : "warning"}>
                {t(`alerts.deliveries.statuses.${d.status}`)}
              </Badge>
              {d.last_error && d.status !== "delivered" && <p className="mt-1 max-w-sm truncate text-xs text-muted-foreground" title={d.last_error}>{d.last_error}</p>}
            </TableCell>
            <TableCell>
              <ol className="flex flex-col gap-0.5 text-xs">
                {d.attempt_log.map((a) => (
                  <li key={a.attempt} className={a.success ? "text-success-text" : "text-destructive-text"} title={a.error || undefined}>
                    {t("alerts.deliveries.attempt", {
                      attempt: a.attempt,
                      status: a.success ? t("alerts.deliveries.attemptOk") : `${t("alerts.deliveries.attemptFailed")}${a.status_code ? ` ${a.status_code}` : ""}`,
                      ms: a.duration_ms,
                    })}
                  </li>
                ))}
              </ol>
            </TableCell>
            <TableCell label={t("alerts.deliveries.columns.created")} className="whitespace-nowrap">
              <DateTimeText value={d.created_at} relative />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
