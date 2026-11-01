// Cloud connection list: every connection of the organization with its provider, what it covers and the
// state of its last poll (docs/contracts/api.md "Cloud connections").
// Router-free: the page passes the navigation callbacks.
import { useQuery } from "@tanstack/react-query";
import { CircleAlert, Plus } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import type { CloudConnection } from "@/api/cloud";
import { cloudConnectionsQuery } from "@/api/cloud";
import { ListPager } from "@/components/ListPager";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { connectionState, firstError, lastRunAt, scopeLabelKey, stateBadgeVariant, type CloudState } from "@/lib/cloud";
import { formatRunAgo } from "@/lib/cloud";
import { timeOf, useLocalList } from "@/lib/list";

export interface CloudConnectionsListProps {
  onOpen: (connection: CloudConnection) => void;
  onNew?: () => void;
  canManage?: boolean;
}

// The worst first when sorted largest first.
const STATE_RANK: Record<CloudState, number> = { paused: 0, unknown: 1, ok: 2, partial: 3, error: 4 };

export function CloudConnectionsList({ onOpen, onNew, canManage = false }: CloudConnectionsListProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(cloudConnectionsQuery());
  const columns = useMemo(
    () => ({
      name: (c: CloudConnection) => c.name,
      provider: (c: CloudConnection) => t(`cloud.providers.${c.provider}`),
      status: (c: CloudConnection) => STATE_RANK[connectionState(c)],
      last_run: (c: CloudConnection) => timeOf(lastRunAt(c.status)),
    }),
    [t],
  );
  const list = useLocalList(q.data?.connections, columns, "name", "asc", 50, "CloudConnectionsList");

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const { connections, secrets_configured: secretsConfigured } = q.data;

  return (
    <div className="flex flex-col gap-3">
      {!secretsConfigured && (
        <div className="flex items-start gap-2 rounded-lg border border-warning/50 bg-warning/10 p-3 text-sm" data-testid="cloud-no-key">
          <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <p className="min-w-0 break-words">{t("cloud.noSecretsKey")}</p>
        </div>
      )}
      {canManage && onNew && secretsConfigured && (
        <div className="flex justify-end">
          <Button type="button" onClick={onNew}>
            <Plus className="size-4" aria-hidden="true" />
            {t("cloud.new")}
          </Button>
        </div>
      )}
      <div className="rounded-xl border bg-card">
        {connections.length === 0 ? (
          <EmptyState>
            <p>{t("cloud.empty")}</p>
            <p className="mt-1 text-xs">{t("cloud.emptyHint")}</p>
          </EmptyState>
        ) : (
          <>
            <Table data-testid="cloud-list">
              <TableHeader>
                <TableRow>
                  <SortableHead column="name" defaultDir="asc" {...list.head}>{t("cloud.columns.name")}</SortableHead>
                  <SortableHead column="provider" defaultDir="asc" {...list.head}>{t("cloud.columns.provider")}</SortableHead>
                  <TableHead className="hidden md:table-cell">{t("cloud.columns.scopes")}</TableHead>
                  <TableHead className="hidden lg:table-cell">{t("cloud.columns.services")}</TableHead>
                  <SortableHead column="status" {...list.head}>{t("cloud.columns.status")}</SortableHead>
                  <SortableHead column="last_run" className="hidden sm:table-cell" {...list.head}>{t("cloud.columns.lastRun")}</SortableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.visible.map((c) => {
                  const state = connectionState(c);
                  const last = lastRunAt(c.status);
                  const error = firstError(c.status);
                  return (
                    <TableRow key={c.id} data-testid="cloud-row">
                      <TableCell>
                        <button type="button" className="font-medium hover:underline" onClick={() => onOpen(c)}>
                          {c.name}
                        </button>
                        {error && <div className="max-w-md truncate text-xs text-muted-foreground" title={error}>{error}</div>}
                      </TableCell>
                      <TableCell>{t(`cloud.providers.${c.provider}`)}</TableCell>
                      <TableCell className="hidden md:table-cell text-xs text-muted-foreground">
                        <span className="sr-only">{t(`cloud.scopeLabelPlural.${scopeLabelKey(c.provider)}`)}: </span>
                        {c.scopes.join(", ")}
                      </TableCell>
                      <TableCell className="hidden lg:table-cell text-xs text-muted-foreground">
                        {c.services.map((s) => t(`cloud.services.${s}`, { defaultValue: s })).join(", ")}
                      </TableCell>
                      <TableCell>
                        <Badge variant={stateBadgeVariant(state)}>{t(`cloud.state.${state}`)}</Badge>
                      </TableCell>
                      <TableCell className="hidden sm:table-cell text-xs text-muted-foreground">
                        {last ? formatRunAgo(last, locale) : t("cloud.detail.never")}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            <ListPager list={list} count={list.visible.length} hasMore={list.hasMore} />
          </>
        )}
      </div>
    </div>
  );
}
