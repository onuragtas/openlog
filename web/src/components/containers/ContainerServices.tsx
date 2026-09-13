import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { containerServicesQuery } from "@/api/containers";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm } from "@/lib/apm";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam, type RangeSpec } from "@/lib/time";

/** APM services whose spans carried this container.id (apm.md §1), with RED metrics over the range. */
export function ContainerServices({ containerId, range }: { containerId: string; range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  const q = useQuery(containerServicesQuery(containerId, range));
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (q.data.length === 0) {
    return <EmptyState icon={<Box className="size-5" aria-hidden="true" />}>{t("containers.detail.services.empty")}</EmptyState>;
  }
  return (
    <Table mobile="stack" data-testid="container-services">
      <TableHeader>
        <TableRow>
          <TableHead>{t("containers.detail.services.columns.service")}</TableHead>
          <TableHead className="text-right">{t("containers.detail.services.columns.throughput")}</TableHead>
          <TableHead className="text-right">{t("containers.detail.services.columns.errorRate")}</TableHead>
          <TableHead className="text-right">{t("containers.detail.services.columns.p95")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("containers.detail.services.columns.lastSeen")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {q.data.map((s) => {
          const seen = parseTimeParam(s.last_seen) ?? 0;
          return (
            <TableRow key={`${s.service_name}|${s.service_namespace}|${s.environment}`}>
              <TableCell>
                <Link
                  to="/apm/services/$service"
                  params={{ service: s.service_name }}
                  search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, ns: s.service_namespace || undefined, env: s.environment || undefined })}
                  className="font-medium hover:underline"
                  aria-label={t("apm.openService", { name: s.service_name })}
                >
                  {s.service_name}
                </Link>
                {(s.environment || s.service_namespace) && <div className="text-xs text-muted-foreground">{[s.environment, s.service_namespace].filter(Boolean).join(" · ")}</div>}
              </TableCell>
              <TableCell label={t("containers.detail.services.columns.throughput")} className="text-right font-mono tabular-nums">
                {formatRpm(s.throughput, locale)}
              </TableCell>
              <TableCell label={t("containers.detail.services.columns.errorRate")} className="text-right font-mono tabular-nums">
                {formatRate(s.error_rate, locale)}
              </TableCell>
              <TableCell label={t("containers.detail.services.columns.p95")} className="text-right font-mono tabular-nums">
                {formatMs(s.p95_ms, locale)}
              </TableCell>
              <TableCell label={t("containers.detail.services.columns.lastSeen")} className="hidden whitespace-nowrap md:table-cell">
                <time dateTime={s.last_seen} title={formatDateTime(seen, locale)}>
                  {formatRelative(seen, now, locale)}
                </time>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
