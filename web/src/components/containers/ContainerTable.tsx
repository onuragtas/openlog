import { Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import type { Container } from "@/api/containers";
import { Sparkline } from "@/components/apm/Charts";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { containerImage, containerName, containerStatus, groupByComposeService, memoryRatio, shortContainerId } from "@/lib/containers";
import { formatBytes, formatDateTime, formatRelative, formatValue } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam } from "@/lib/time";

export function ContainerStatusBadge({ container }: { container: Pick<Container, "state" | "health" | "reporting"> }) {
  const { t } = useTranslation();
  const s = containerStatus(container);
  const health = container.health && s.key !== "unhealthy" ? container.health : "";
  return (
    <Badge variant={s.variant} data-testid="container-status" title={health ? t(`containers.health.${health as "healthy"}`, { defaultValue: health }) : undefined}>
      {t(`containers.states.${s.key}`)}
    </Badge>
  );
}

/** "18.4 MiB / 512 MiB" (memory of the latest bucket against the limit). */
function MemoryCell({ c }: { c: Container }) {
  const { i18n } = useTranslation();
  if (c.memory_usage == null) return <span className="text-muted-foreground">–</span>;
  const ratio = memoryRatio(c.memory_usage, c.memory_limit);
  return (
    <span className="whitespace-nowrap">
      {formatBytes(c.memory_usage)}
      {ratio != null && <span className="text-muted-foreground"> · {formatValue(ratio, "percent", i18n.resolvedLanguage)}</span>}
    </span>
  );
}

export interface ContainerTableProps {
  containers: Container[];
  showHost?: boolean;
  showCompose?: boolean;
}

/** Container rows; on phones every row becomes a card (Table mobile="stack"). */
export function ContainerTable({ containers, showHost = true, showCompose = true }: ContainerTableProps) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  return (
    <Table mobile="stack" data-testid="container-table">
      <TableHeader>
        <TableRow>
          <TableHead>{t("containers.columns.name")}</TableHead>
          <TableHead>{t("containers.columns.state")}</TableHead>
          <TableHead className="hidden xl:table-cell">{t("containers.columns.image")}</TableHead>
          {showCompose && <TableHead className="hidden lg:table-cell">{t("containers.columns.compose")}</TableHead>}
          {showHost && <TableHead className="hidden md:table-cell">{t("containers.columns.host")}</TableHead>}
          <TableHead className="text-right">{t("containers.columns.cpu")}</TableHead>
          <TableHead className="hidden lg:table-cell">{t("containers.columns.trend")}</TableHead>
          <TableHead className="text-right">{t("containers.columns.memory")}</TableHead>
          <TableHead className="hidden md:table-cell">{t("containers.columns.lastSeen")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {containers.map((c) => {
          const name = containerName(c);
          const seen = parseTimeParam(c.last_seen) ?? 0;
          const linkSearch = (prev: Record<string, unknown>) => ({ range: prev.range, from: prev.from, to: prev.to }) as never;
          return (
            <TableRow
              key={`${c.host_id}/${c.container_id}`}
              className="cursor-pointer"
              onClick={(e) => {
                if ((e.target as HTMLElement).closest("a")) return;
                void navigate({ to: "/containers/$containerId", params: { containerId: c.container_id }, search: linkSearch });
              }}
            >
              <TableCell className="min-w-0">
                <Link
                  to="/containers/$containerId"
                  params={{ containerId: c.container_id }}
                  search={linkSearch}
                  className="font-medium break-all hover:underline"
                  aria-label={t("containers.open", { name })}
                >
                  {name}
                </Link>
                <div className="font-mono text-xs text-muted-foreground">
                  {shortContainerId(c.container_id)}
                  <span className="xl:hidden"> · {containerImage(c)}</span>
                </div>
              </TableCell>
              <TableCell label={t("containers.columns.state")}>
                <ContainerStatusBadge container={c} />
              </TableCell>
              <TableCell label={t("containers.columns.image")} className="hidden font-mono text-xs break-all xl:table-cell">
                {containerImage(c) || "–"}
              </TableCell>
              {showCompose && (
                <TableCell label={t("containers.columns.compose")} className="hidden lg:table-cell">
                  {c.compose_service ? (
                    <span className="text-sm">
                      {c.compose_service}
                      <span className="block text-xs text-muted-foreground">{c.compose_project}</span>
                    </span>
                  ) : (
                    <span className="text-muted-foreground">–</span>
                  )}
                </TableCell>
              )}
              {showHost && (
                <TableCell label={t("containers.columns.host")} className="hidden md:table-cell">
                  <Link to="/hosts/$hostId" params={{ hostId: c.host_id }} search={linkSearch} className="hover:underline">
                    {c.host_name || c.host_id}
                  </Link>
                </TableCell>
              )}
              <TableCell label={t("containers.columns.cpu")} className="text-right font-mono tabular-nums">
                {formatValue(c.cpu_utilization, "percent", locale)}
              </TableCell>
              <TableCell className="hidden lg:table-cell">
                <Sparkline points={c.cpu_sparkline as [number, number][]} label={t("containers.columns.trend")} />
              </TableCell>
              <TableCell label={t("containers.columns.memory")} className="text-right font-mono tabular-nums">
                <MemoryCell c={c} />
              </TableCell>
              <TableCell label={t("containers.columns.lastSeen")} className="hidden whitespace-nowrap md:table-cell">
                <time dateTime={c.last_seen} title={formatDateTime(seen, locale)}>
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

/** Containers grouped by compose project and service, each group with running count and summed CPU/memory. */
export function ContainerGroups({ containers, showHost = true }: { containers: Container[]; showHost?: boolean }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  return (
    <div className="flex flex-col divide-y" data-testid="container-groups">
      {groupByComposeService(containers).map((g) => {
        const headingId = `container-group-${g.key || "standalone"}`;
        return (
          <section key={g.key} aria-labelledby={headingId} className="py-2">
            <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-3 py-1">
              <h2 id={headingId} className="text-sm font-semibold break-all">
                {g.key === "" ? (
                  t("containers.ungrouped")
                ) : (
                  <>
                    {g.service}
                    {g.project && (
                      <>
                        {" · "}
                        <span className="font-normal text-muted-foreground">{g.project}</span>
                      </>
                    )}
                  </>
                )}
              </h2>
              <p className="flex flex-wrap gap-x-3 text-xs text-muted-foreground">
                <span>{t("containers.running", { running: g.running, count: g.containers.length })}</span>
                {g.cpu != null && (
                  <span>
                    {t("containers.columns.cpu")} {formatValue(g.cpu, "percent", locale)}
                  </span>
                )}
                {g.memory != null && (
                  <span>
                    {t("containers.columns.memory")} {formatBytes(g.memory)}
                  </span>
                )}
              </p>
            </div>
            <ContainerTable containers={g.containers} showHost={showHost} showCompose={false} />
          </section>
        );
      })}
    </div>
  );
}
