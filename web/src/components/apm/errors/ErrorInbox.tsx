// Error inbox (apm.md §3.4): status tabs with counts, assignee/search/sort filters, selectable groups with
// bulk workflow actions. Service inbox (scope) or organization-wide (GET /apm/errors, with a service column).
import { useQuery } from "@tanstack/react-query";
import { MessageSquare, Search } from "lucide-react";
import { useId, useMemo, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { apmErrorInboxQuery, apmErrorsQuery, type ApmErrorGroup, type ErrorSort } from "@/api/apm";
import { Sparkline } from "@/components/apm/Charts";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { ServiceScope } from "@/lib/apm";
import { allSelected, ERROR_SORTS, ERROR_STATUS_TABS, pruneSelection, toggleAll, type ErrorStatusTab } from "@/lib/apm-errors";
import { formatDateTime, formatNumber, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam, type RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";
import { StatusBadges } from "./StatusBadges";
import { useErrorWorkflow, WorkflowActions } from "./WorkflowActions";

export interface ErrorInboxFilterValues {
  status: ErrorStatusTab;
  /** any | me | none | user id */
  assignee: string;
  q: string;
  sort: ErrorSort;
}

export const DEFAULT_INBOX_FILTERS: ErrorInboxFilterValues = { status: "unresolved", assignee: "any", q: "", sort: "count" };

export interface ErrorInboxProps {
  range: RangeSpec;
  /** service inbox; omitted = organization-wide inbox */
  scope?: ServiceScope;
  /** organization-wide filters */
  orgFilter?: { service?: string; namespace?: string; environment?: string };
  filters: ErrorInboxFilterValues;
  onFilters: (patch: Partial<ErrorInboxFilterValues>) => void;
  selected?: string;
  onOpen: (group: ApmErrorGroup) => void;
  /** prefill of "Resolve in version…" */
  defaultVersion?: string;
}

export function ErrorInbox({ range, scope, orgFilter, filters, onFilters, selected, onOpen, defaultVersion }: ErrorInboxProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  const ids = { q: useId(), assignee: useId(), sort: useId() };
  const orgWide = !scope;
  const { canWrite, members } = useErrorWorkflow();
  const inboxFilters = { status: filters.status, assignee: filters.assignee, q: filters.q, sort: filters.sort };
  const q = useQuery(scope ? apmErrorsQuery(scope, range, inboxFilters) : apmErrorInboxQuery(range, { ...orgFilter, ...inboxFilters }));
  const [draftQ, setDraftQ] = useState(filters.q);
  const [shownQ, setShownQ] = useState(filters.q);
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  // The URL filter changed elsewhere (navigation): show it in the search box.
  if (shownQ !== filters.q) {
    setShownQ(filters.q);
    setDraftQ(filters.q);
  }

  const groups = useMemo(() => q.data?.groups ?? [], [q.data]);
  const visibleIds = useMemo(() => groups.map((g) => g.group_id), [groups]);
  const selection = useMemo(() => pruneSelection(picked, visibleIds), [picked, visibleIds]);
  const workflow = q.data?.workflow ?? true;
  const writable = canWrite && workflow;
  const counts = q.data?.counts;
  const countOf = (s: ErrorStatusTab) => (counts ? (s === "all" ? counts.unresolved + counts.resolved + counts.ignored : counts[s]) : undefined);

  const submitSearch = (e: FormEvent) => {
    e.preventDefault();
    onFilters({ q: draftQ.trim() });
  };

  return (
    <Card data-testid="error-inbox">
      <CardHeader className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>
            <h2>{orgWide ? t("apm.errors.inboxTitle") : t("apm.service.tabs.errors")}</h2>
          </CardTitle>
          {q.data?.truncated && <p className="text-xs text-muted-foreground">{t("apm.errors.truncated")}</p>}
        </div>
        {workflow ? (
          <div role="group" aria-label={t("apm.errors.statusFilter")} className="flex flex-wrap gap-1" data-testid="error-status-tabs">
            {ERROR_STATUS_TABS.map((s) => {
              const n = countOf(s);
              const active = filters.status === s;
              return (
                <Button key={s} type="button" size="sm" variant={active ? "secondary" : "ghost"} aria-pressed={active} onClick={() => onFilters({ status: s })}>
                  {t(`apm.errors.statuses.${s}`)}
                  {n !== undefined && (
                    <Badge variant={active ? "default" : "muted"} className="tabular-nums">
                      {formatNumber(n, locale)}
                    </Badge>
                  )}
                </Button>
              );
            })}
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">{t("apm.errors.noWorkflow")}</p>
        )}
        <div className="flex flex-wrap items-end gap-2">
          <form onSubmit={submitSearch} className="relative w-full sm:w-64" role="search">
            <label htmlFor={ids.q} className="sr-only">
              {t("apm.errors.search")}
            </label>
            <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
            <Input
              id={ids.q}
              type="search"
              className="pl-8"
              placeholder={t("apm.errors.searchPlaceholder")}
              value={draftQ}
              onChange={(e) => setDraftQ(e.target.value)}
              onBlur={() => draftQ.trim() !== filters.q && onFilters({ q: draftQ.trim() })}
            />
          </form>
          {workflow && (
            <div className="flex items-center gap-2">
              <label htmlFor={ids.assignee} className="text-xs text-muted-foreground">
                {t("apm.errors.assigneeFilter")}
              </label>
              <NativeSelect id={ids.assignee} value={filters.assignee} onChange={(e) => onFilters({ assignee: e.target.value })} className="max-w-[14rem]">
                <option value="any">{t("apm.errors.anyone")}</option>
                <option value="me">{t("apm.errors.assignedToMe")}</option>
                <option value="none">{t("apm.errors.unassigned")}</option>
                {(members ?? []).map((m) => (
                  <option key={m.user_id} value={m.user_id}>
                    {m.name || m.email}
                  </option>
                ))}
              </NativeSelect>
            </div>
          )}
          <div className="flex items-center gap-2">
            <label htmlFor={ids.sort} className="text-xs text-muted-foreground">
              {t("apm.errors.sort")}
            </label>
            <NativeSelect id={ids.sort} value={filters.sort} onChange={(e) => onFilters({ sort: e.target.value as ErrorSort })}>
              {ERROR_SORTS.map((s) => (
                <option key={s} value={s}>
                  {t(`apm.errors.sorts.${s}`)}
                </option>
              ))}
            </NativeSelect>
          </div>
        </div>
        {writable && selection.size > 0 && (
          <div className="flex flex-col gap-2 rounded-lg border bg-muted/40 p-2" data-testid="error-bulk-actions">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="font-medium" aria-live="polite">
                {t("apm.errors.selectedCount", { count: selection.size })}
              </span>
              <Button size="sm" variant="ghost" onClick={() => setPicked(new Set())}>
                {t("apm.errors.clearSelection")}
              </Button>
            </div>
            <WorkflowActions
              key={[...selection].join(",")}
              label={t("apm.errors.bulkActions")}
              groupIds={[...selection]}
              defaultVersion={defaultVersion}
              members={members}
              onDone={() => setPicked(new Set())}
            />
          </div>
        )}
      </CardHeader>
      <CardContent className="px-0">
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : groups.length === 0 ? (
          <EmptyState>{filters.q || filters.assignee !== "any" || filters.status !== "all" ? t("apm.errors.emptyFiltered") : t("apm.errors.empty")}</EmptyState>
        ) : (
          <Table data-testid="error-groups">
            <TableHeader>
              <TableRow>
                {writable && (
                  <TableHead className="w-10">
                    <input
                      type="checkbox"
                      className="size-4 align-middle pointer-coarse:size-5"
                      aria-label={t("apm.errors.selectAll")}
                      checked={allSelected(selection, visibleIds)}
                      onChange={() => setPicked(toggleAll(selection, visibleIds))}
                    />
                  </TableHead>
                )}
                <TableHead>{t("apm.errors.error")}</TableHead>
                {orgWide && <TableHead>{t("apm.errors.service")}</TableHead>}
                <TableHead>{t("apm.errors.status")}</TableHead>
                {workflow && <TableHead className="hidden md:table-cell">{t("apm.errors.assignee")}</TableHead>}
                {workflow && (
                  <TableHead className="hidden text-right md:table-cell">
                    <MessageSquare className="ml-auto size-4" aria-label={t("apm.errors.comments")} />
                  </TableHead>
                )}
                <TableHead className="hidden lg:table-cell">{t("apm.errors.trend")}</TableHead>
                <TableHead className="text-right">{t("apm.errors.count")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("apm.errors.lastSeen")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {groups.map((g) => {
                const last = parseTimeParam(g.last_seen);
                const isSelected = g.group_id === selected;
                return (
                  <TableRow key={g.group_id} data-state={isSelected ? "selected" : undefined}>
                    {writable && (
                      <TableCell>
                        <input
                          type="checkbox"
                          className="size-4 align-middle pointer-coarse:size-5"
                          aria-label={t("apm.errors.select", { type: g.error_type })}
                          checked={selection.has(g.group_id)}
                          onChange={() =>
                            setPicked(() => {
                              const next = new Set(selection);
                              if (next.has(g.group_id)) next.delete(g.group_id);
                              else next.add(g.group_id);
                              return next;
                            })
                          }
                        />
                      </TableCell>
                    )}
                    <TableCell className="max-w-[32rem]">
                      <button
                        type="button"
                        className="flex max-w-full flex-col items-start text-left max-md:max-w-[50vw]"
                        aria-label={t("apm.errors.open", { type: g.error_type })}
                        aria-pressed={isSelected}
                        onClick={() => onOpen(g)}
                      >
                        <span className="font-mono text-sm font-semibold text-destructive-text hover:underline">{g.error_type}</span>
                        <span className="max-w-full truncate text-sm" title={g.message}>
                          {g.message || "–"}
                        </span>
                        {g.last_span_name && <span className="text-xs text-muted-foreground">{g.last_span_name}</span>}
                      </button>
                    </TableCell>
                    {orgWide && (
                      <TableCell className="text-sm">
                        <span className="font-medium">{g.service_name}</span>
                        {(g.environment || g.service_namespace) && <div className="text-xs text-muted-foreground">{[g.environment, g.service_namespace].filter(Boolean).join(" · ")}</div>}
                      </TableCell>
                    )}
                    <TableCell>
                      <StatusBadges group={g} />
                    </TableCell>
                    {workflow && <TableCell className="hidden text-sm md:table-cell">{g.assignee ? g.assignee.name || g.assignee.email : <span className="text-muted-foreground">–</span>}</TableCell>}
                    {workflow && (
                      <TableCell className={cn("hidden text-right font-mono tabular-nums md:table-cell", g.comment_count === 0 && "text-muted-foreground")}>
                        <span aria-label={t("apm.errors.commentCount", { count: g.comment_count })}>{formatNumber(g.comment_count, locale)}</span>
                      </TableCell>
                    )}
                    <TableCell className="hidden lg:table-cell">
                      <Sparkline points={g.sparkline} label={t("apm.errors.trend")} />
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatNumber(g.count, locale)}</TableCell>
                    <TableCell className="hidden whitespace-nowrap text-xs md:table-cell">
                      {last !== null ? (
                        <time dateTime={g.last_seen ?? undefined} title={formatDateTime(last, locale)}>
                          {formatRelative(last, now, locale)}
                        </time>
                      ) : (
                        "–"
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
