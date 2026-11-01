import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { containerServicesQuery, type ContainerService } from "@/api/containers";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { SortableHead, Table, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm } from "@/lib/apm";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { timeOf, useLocalList } from "@/lib/list";
import { parseTimeParam, type RangeSpec } from "@/lib/time";

/** APM services whose spans carried this container.id (apm.md §1), with RED metrics over the range. */
const SERVICE_COLUMNS = {
  service: (s: ContainerService) => s.service_name,
  throughput: (s: ContainerService) => s.throughput,
  error_rate: (s: ContainerService) => s.error_rate,
  p95: (s: ContainerService) => s.p95_ms,
  last_seen: (s: ContainerService) => timeOf(s.last_seen),
};

export function ContainerServices({ containerId, range }: { containerId: string; range: RangeSpec }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  const q = useQuery(containerServicesQuery(containerId, range));
  const list = useLocalList(q.data, SERVICE_COLUMNS, "throughput", "desc");
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (q.data.length === 0) {
    return <EmptyState icon={<Box className="size-5" aria-hidden="true" />}>{t("containers.detail.services.empty")}</EmptyState>;
  }
  return (
    <>
      <Table mobile="stack" data-testid="container-services">
        <TableHeader>
          <TableRow>
            <SortableHead column="service" defaultDir="asc" {...list.head}>{t("containers.detail.services.columns.service")}</SortableHead>
            <SortableHead column="throughput" className="text-right" {...list.head}>{t("containers.detail.services.columns.throughput")}</SortableHead>
            <SortableHead column="error_rate" className="text-right" {...list.head}>{t("containers.detail.services.columns.errorRate")}</SortableHead>
            <SortableHead column="p95" className="text-right" {...list.head}>{t("containers.detail.services.columns.p95")}</SortableHead>
            <SortableHead column="last_seen" className="hidden md:table-cell" {...list.head}>{t("containers.detail.services.columns.lastSeen")}</SortableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.visible.map((s) => {
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
      <ListPager list={list} count={list.visible.length} hasMore={list.hasMore} />
    </>
  );
}
