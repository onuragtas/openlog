import { useQueries, useQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { BellPlus } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { fleetHostQuery } from "@/api/fleet";
import { hostQuery, metricQuery } from "@/api/queries";
import { PHPAccessNotice } from "@/components/onboarding/PHPAccessNotice";
import { JavaAgentPanel } from "@/components/fleet/JavaAgentPanel";
import { usePermissions } from "@/lib/org-writable";
import { can } from "@/api/roles";
import { WriteGuard, WriteGuardLink } from "@/components/ReadOnly";
import { Button, buttonVariants } from "@/components/ui/button";
import { createAlertSearch } from "@/lib/alerts";
import type { Aggregation } from "@/api/types";
import { TimeSeriesChart } from "@/components/TimeSeriesChart";
import { AddToDashboardButton } from "@/components/oql/AddToDashboardButton";
import { hostOsOf } from "@/lib/host-os";
import { hostMetricOql } from "@/lib/oql";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { UnitKind } from "@/lib/format";
import { fromMetricSeries, type ChartSeriesInput } from "@/lib/series";
import type { RangeSpec } from "@/lib/time";

const route = getRouteApi("/app/hosts/$hostId");

interface MetricSpec {
  name: string;
  agg: Aggregation;
  groupBy?: string[];
  /** Fixed label for all series of this metric (used when combining metrics). */
  label?: string;
}

interface ChartDef {
  id: string;
  titleKey: "charts.cpu" | "charts.load" | "charts.memory" | "charts.filesystem" | "charts.diskIO" | "charts.networkIO";
  metrics: MetricSpec[];
  unit: UnitKind;
  stacked?: boolean;
  order?: string[];
  yMax?: number;
  yCap?: number;
  /** Series hidden until enabled in the legend. */
  hidden?: readonly string[];
}

// Metric names/attributes: docs/contracts/semantic-conventions.md §2.
export const OVERVIEW_CHARTS: ChartDef[] = [
  {
    id: "cpu",
    titleKey: "charts.cpu",
    metrics: [{ name: "system.cpu.utilization", agg: "avg", groupBy: ["cpu.mode"] }],
    unit: "percent",
    stacked: true,
    order: ["user", "system", "iowait", "nice", "irq", "interrupt", "softirq", "steal", "idle"],
    // A stacked idle area hides everything else; show busy modes, auto-scaled (max 100%).
    hidden: ["idle"],
    yCap: 1,
  },
  {
    id: "load",
    titleKey: "charts.load",
    metrics: [
      { name: "system.cpu.load_average.1m", agg: "avg", label: "1m" },
      { name: "system.cpu.load_average.5m", agg: "avg", label: "5m" },
      { name: "system.cpu.load_average.15m", agg: "avg", label: "15m" },
    ],
    unit: "number",
  },
  {
    id: "memory",
    titleKey: "charts.memory",
    metrics: [{ name: "system.memory.usage", agg: "avg", groupBy: ["system.memory.state"] }],
    unit: "bytes",
    stacked: true,
    order: ["used", "buffers", "cached", "free"],
  },
  {
    id: "filesystem",
    titleKey: "charts.filesystem",
    metrics: [{ name: "system.filesystem.utilization", agg: "avg", groupBy: ["system.filesystem.mountpoint"] }],
    unit: "percent",
    yMax: 1,
  },
  {
    id: "disk",
    titleKey: "charts.diskIO",
    metrics: [{ name: "system.disk.io", agg: "rate", groupBy: ["disk.io.direction"] }],
    unit: "bytesPerSec",
    order: ["read", "write"],
  },
  {
    id: "network",
    titleKey: "charts.networkIO",
    metrics: [{ name: "system.network.io", agg: "rate", groupBy: ["network.io.direction"] }],
    unit: "bytesPerSec",
    order: ["receive", "transmit"],
  },
];

/** One host metric chart; `canAlert` (role) shows the alert and dashboard shortcuts, disabled in a read-only organization. */
export function MetricChartCard({ hostId, range, def, canAlert }: { hostId: string; range: RangeSpec; def: ChartDef; canAlert: boolean }) {
  const { t } = useTranslation();
  const title = t(def.titleKey);
  const hostName = useQuery({ ...hostQuery(hostId), enabled: canAlert }).data?.host_name;
  const alertMetric = def.metrics[0]!;
  const results = useQueries({
    queries: def.metrics.map((m) => metricQuery({ hostId, name: m.name, range, agg: m.agg, groupBy: m.groupBy })),
  });

  const isLoading = results.some((r) => r.isPending);
  const error = results.find((r) => r.isError)?.error;
  const dataKey = results.map((r) => r.dataUpdatedAt).join(",");
  const series = useMemo<ChartSeriesInput[] | undefined>(() => {
    if (results.some((r) => !r.data)) return undefined;
    return results.flatMap((r, i) => {
      const spec = def.metrics[i]!;
      const s = fromMetricSeries(r.data!.series, { keys: spec.groupBy, fallbackLabel: spec.label ?? spec.name });
      return spec.label ? s.map((x) => ({ ...x, label: spec.label! })) : s;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataKey, def]);
  const first = results[0]?.data;

  return (
    <Card className="min-w-0 gap-2">
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle>
          <h2>{title}</h2>
        </CardTitle>
        {canAlert && (
          <WriteGuard className="ml-auto">
            <AddToDashboardButton className="ml-auto" query={hostMetricOql(alertMetric.name, hostId, alertMetric.groupBy)} title={title} />
          </WriteGuard>
        )}
        {canAlert && (
          <WriteGuardLink
            disabled={
              <Button type="button" variant="ghost" size="icon" className="-my-2 size-7" aria-label={`${t("charts.createAlert")}: ${title}`}>
                <BellPlus aria-hidden="true" />
              </Button>
            }
          >
            <Link
              to="/alerts/rules/new"
              search={createAlertSearch({ metric: alertMetric.name, hostId, hostName, agg: alertMetric.agg }) as never}
              aria-label={`${t("charts.createAlert")}: ${title}`}
              title={t("charts.createAlert")}
              className={buttonVariants({ variant: "ghost", size: "icon", className: "-my-2 size-7" })}
            >
              <BellPlus aria-hidden="true" />
            </Link>
          </WriteGuardLink>
        )}
      </CardHeader>
      <CardContent>
        <TimeSeriesChart
          title={title}
          series={series}
          unit={def.unit}
          stacked={def.stacked}
          order={def.order}
          yMax={def.yMax}
          yCap={def.yCap}
          hidden={def.hidden}
          from={first?.from}
          to={first?.to}
          isLoading={isLoading}
          error={error}
          onRetry={() => results.forEach((r) => void r.refetch())}
        />
      </CardContent>
    </Card>
  );
}

export function HostOverviewTab({ hostId }: { hostId: string }) {
  const search = route.useSearch();
  const range: RangeSpec = { range: search.range, from: search.from, to: search.to };
  const me = useMe().data;
  const canAlert = can(me?.role, "alerts.write");
  const canManageFleet = usePermissions().can("fleet.manage");
  // PHP-FPM pools that cannot write the agent's socket lose their spans silently; show the fix on the host page too.
  const fleetHost = useQuery({ ...fleetHostQuery(hostId), enabled: !!me }).data;
  const phpAccess = fleetHost?.php_access;
  const os = hostOsOf(useQuery(hostQuery(hostId)).data);
  return (
    <div className="flex flex-col gap-4">
      {phpAccess && <PHPAccessNotice access={phpAccess} os={os} />}
      {/* JVMs with the openlog Java agent and the jar the infra agent keeps current (java-agent.md §2). */}
      {fleetHost?.java_agent && <JavaAgentPanel host={fleetHost} canManage={canManageFleet} />}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {OVERVIEW_CHARTS.map((def) => (
          <MetricChartCard key={def.id} hostId={hostId} range={range} def={def} canAlert={canAlert} />
        ))}
      </div>
    </div>
  );
}
