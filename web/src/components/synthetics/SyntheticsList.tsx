// Synthetic check list: every check of the organization with its current state, uptime and a latency
// sparkline over the last 24 hours (docs/contracts/api.md "Synthetic monitoring").
// Router-free: the page passes the navigation callbacks.
import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { SyntheticCheckListItem } from "@/api/synthetics";
import { syntheticChecksQuery } from "@/api/synthetics";
import { Sparkline } from "@/components/apm/Charts";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { checkKind, checkState, checkTarget, formatDuration, formatRunAgo, formatUptime, lastRunAt, latencyPoints, stateBadgeVariant } from "@/lib/synthetics";

export interface SyntheticsListProps {
  onOpen: (check: SyntheticCheckListItem) => void;
  onNew?: () => void;
  canWrite?: boolean;
}

export function SyntheticsList({ onOpen, onNew, canWrite = false }: SyntheticsListProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(syntheticChecksQuery());

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const checks = q.data.checks;

  return (
    <div className="flex flex-col gap-3">
      {canWrite && onNew && (
        <div className="flex justify-end">
          <Button type="button" onClick={onNew}>
            <Plus className="size-4" aria-hidden="true" />
            {t("synthetics.new")}
          </Button>
        </div>
      )}
      <div className="rounded-xl border bg-card">
        {checks.length === 0 ? (
          <EmptyState>
            <p>{t("synthetics.empty")}</p>
            <p className="mt-1 text-xs">{t("synthetics.emptyHint")}</p>
          </EmptyState>
        ) : (
          <Table data-testid="synthetics-list">
            <TableHeader>
              <TableRow>
                <TableHead>{t("synthetics.columns.name")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("synthetics.columns.target")}</TableHead>
                <TableHead>{t("synthetics.columns.status")}</TableHead>
                <TableHead className="text-right">{t("synthetics.columns.uptime")}</TableHead>
                <TableHead className="hidden lg:table-cell text-right">{t("synthetics.columns.latency")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("synthetics.window")}</TableHead>
                <TableHead className="hidden sm:table-cell">{t("synthetics.columns.lastRun")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {checks.map((c) => {
                const state = checkState(c);
                const last = lastRunAt(c.status);
                return (
                  <TableRow key={c.id} data-testid="synthetics-row">
                    <TableCell>
                      <button type="button" className="font-medium hover:underline" onClick={() => onOpen(c)}>
                        {c.name}
                      </button>
                      <div className="text-xs text-muted-foreground">
                        {t("synthetics.window")}
                        {` · ${c.interval_seconds}s`}
                      </div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell text-xs">
                      {/* The kind before the target: "shop.example.com:443" means something different for a
                          TCP check than for a TLS one. */}
                      <Badge variant="outline" className="mr-1.5 align-middle">
                        {t(`synthetics.kinds.${checkKind(c.type)}`)}
                      </Badge>
                      <span className="font-mono">{checkTarget(c)}</span>
                    </TableCell>
                    <TableCell>
                      <Badge variant={stateBadgeVariant(state)}>{t(`synthetics.state.${state}`)}</Badge>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatUptime(c.summary?.uptime, locale)}</TableCell>
                    <TableCell className="hidden lg:table-cell text-right font-mono tabular-nums">
                      {formatDuration(c.summary?.p95_ms, locale)}
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      <Sparkline points={latencyPoints(c.summary)} label={`${c.name} ${t("synthetics.columns.latency")}`} />
                    </TableCell>
                    <TableCell className="hidden sm:table-cell text-xs text-muted-foreground">
                      {last ? formatRunAgo(last, locale) : t("synthetics.detail.never")}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}
