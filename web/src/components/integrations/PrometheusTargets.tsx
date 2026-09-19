import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CircleCheck, CircleX, LineChart } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { PROMETHEUS_MAX_TARGETS, prometheusTargetsQuery } from "@/api/prometheus";
import { EmptyState, ErrorState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { encodeMetricQueries, newMetricQuery } from "@/lib/metrics-explorer";
import { targetExplorerFilters, type PrometheusTarget } from "@/lib/prometheus";

function explorerSearch(target: PrometheusTarget) {
  const q = { ...newMetricQuery("A", "up"), filters: targetExplorerFilters(target), aggregation: "last" as const };
  return { range: "1h", mq: encodeMetricQueries([q]) };
}

/**
 * Prometheus/OpenMetrics targets of every host (D-137): up or down with the time of the last scrape. Hidden while no
 * agent scrapes anything, so the Integrations page stays unchanged for tenants that do not use it.
 */
export function PrometheusTargetsCard() {
  const { t, i18n } = useTranslation();
  const q = useQuery(prometheusTargetsQuery());
  const now = useNow();
  const down = useMemo(() => q.data?.targets.filter((x) => !x.up).length ?? 0, [q.data]);
  const notFound = q.isError && (q.error as { status?: number }).status === 404;
  // Nothing while loading either: most tenants scrape nothing, and a card that flashes and vanishes is noise.
  if (q.isPending || notFound || (q.isSuccess && q.data.targets.length === 0)) return null;
  const locale = i18n.resolvedLanguage ?? "en";
  return (
    <Card className="mb-4 min-w-0 gap-2" data-testid="prometheus-targets">
      <CardHeader>
        <CardTitle>
          <h2 className="flex flex-wrap items-center gap-2">
            {t("prometheus.title")}
            {q.isSuccess && (
              <Badge variant={down > 0 ? "destructive" : "muted"} data-testid="prometheus-summary">
                {down > 0 ? t("prometheus.down", { count: down }) : t("prometheus.allUp", { count: q.data.targets.length })}
              </Badge>
            )}
          </h2>
        </CardTitle>
        <CardDescription>{t("prometheus.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.data.targets.length === 0 ? (
          <EmptyState>{t("prometheus.empty")}</EmptyState>
        ) : (
          <>
            <Table mobile="stack">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("prometheus.columns.state")}</TableHead>
                  <TableHead>{t("prometheus.columns.job")}</TableHead>
                  <TableHead>{t("prometheus.columns.instance")}</TableHead>
                  <TableHead>{t("prometheus.columns.host")}</TableHead>
                  <TableHead>{t("prometheus.columns.source")}</TableHead>
                  <TableHead>{t("prometheus.columns.lastScrape")}</TableHead>
                  <TableHead>
                    <span className="sr-only">{t("prometheus.openMetrics")}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {q.data.targets.map((x) => (
                  <TableRow key={`${x.hostId}/${x.job}/${x.instance}`} data-testid="prometheus-target" data-up={x.up}>
                    <TableCell>
                      {x.up ? (
                        <span className="inline-flex items-center gap-1 text-success-text">
                          <CircleCheck className="size-4" aria-hidden="true" />
                          {t("prometheus.up")}
                        </span>
                      ) : (
                        <span className="inline-flex items-center gap-1 text-destructive-text">
                          <CircleX className="size-4" aria-hidden="true" />
                          {t("prometheus.downOne")}
                        </span>
                      )}
                    </TableCell>
                    <TableCell label={t("prometheus.columns.job")} className="font-medium break-all">
                      {x.job || "–"}
                    </TableCell>
                    <TableCell label={t("prometheus.columns.instance")} className="font-mono text-xs break-all">
                      {x.instance || "–"}
                    </TableCell>
                    <TableCell label={t("prometheus.columns.host")}>
                      {x.hostId ? (
                        <Link to="/hosts/$hostId" params={{ hostId: x.hostId }} className="text-primary hover:underline">
                          {x.hostName}
                        </Link>
                      ) : (
                        "–"
                      )}
                    </TableCell>
                    <TableCell label={t("prometheus.columns.source")}>{x.source ? t(`prometheus.sources.${x.source}`) : "–"}</TableCell>
                    <TableCell label={t("prometheus.columns.lastScrape")} className="tabular-nums">
                      {formatRelative(x.lastSeen, now, locale)}
                    </TableCell>
                    <TableCell className="text-right">
                      <Link
                        to="/metrics"
                        search={explorerSearch(x) as never}
                        aria-label={t("prometheus.openMetricsFor", { job: x.job, instance: x.instance })}
                        className={buttonVariants({ variant: "outline", size: "sm", className: "min-h-10" })}
                      >
                        <LineChart aria-hidden="true" />
                        {t("prometheus.openMetrics")}
                      </Link>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            {q.data.truncated && <p className="mt-2 text-xs text-muted-foreground">{t("prometheus.truncated", { count: PROMETHEUS_MAX_TARGETS })}</p>}
          </>
        )}
      </CardContent>
    </Card>
  );
}
