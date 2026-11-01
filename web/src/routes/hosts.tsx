import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useEffect, useId } from "react";
import { useTranslation } from "react-i18next";
import { hostListQuery, type HostSort } from "@/api/queries";
import { AttributeChips } from "@/components/AttributeChips";
import { PageHeader } from "@/components/AppShell";
import { ListPager } from "@/components/ListPager";
import { AddDataLink } from "@/components/onboarding/AddDataLink";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Input } from "@/components/ui/input";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatLoad, UsageBar } from "@/components/hosts/UsageBar";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { useListState } from "@/lib/list";
import { parseTimeParam } from "@/lib/time";
import { useDebounced } from "@/lib/use-debounced";

const route = getRouteApi("/app/hosts");

export function HostsPage() {
  const { t, i18n } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/hosts" });
  const id = useId();
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = search.q ?? "";
  // Searched on the server, since the page holds only some of the hosts.
  const debounced = useDebounced(q, 250);
  const list = useListState<HostSort>("name", "asc", 50, "hosts");
  const query = useQuery(hostListQuery(list.state, debounced));
  const { toFirstPage } = list;
  useEffect(toFirstPage, [debounced, toFirstPage]);
  const hosts = query.data?.hosts ?? [];

  return (
    <div>
      <PageHeader
        title={t("hosts.title")}
        subtitle={t("hosts.subtitle")}
        actions={
          <div className="relative w-full sm:w-72">
            <label htmlFor={id} className="sr-only">
              {t("hosts.searchLabel")}
            </label>
            <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" aria-hidden="true" />
            <Input
              id={id}
              type="search"
              className="pl-8"
              placeholder={t("hosts.searchPlaceholder")}
              value={q}
              onChange={(e) => void navigate({ search: (prev) => ({ ...prev, q: e.target.value || undefined }), replace: true })}
            />
          </div>
        }
      />
      <div className="rounded-xl border bg-card">
        {query.isPending ? (
          <LoadingState />
        ) : query.isError ? (
          <ErrorState error={query.error} onRetry={() => void query.refetch()} />
        ) : hosts.length === 0 && list.state.offset === 0 ? (
          debounced ? (
            <EmptyState>{t("hosts.noMatch", { q: debounced })}</EmptyState>
          ) : (
            <EmptyState>
              <p>{t("hosts.empty")}</p>
              <AddDataLink target="linux" label={t("addData.empty.hosts")} />
            </EmptyState>
          )
        ) : (
          <>
            <Table mobile="stack">
              <TableHeader>
                <TableRow>
                  <SortableHead column="name" defaultDir="asc" {...list.head}>{t("hosts.columns.name")}</SortableHead>
                  <SortableHead column="os" defaultDir="asc" {...list.head}>{t("hosts.columns.os")}</SortableHead>
                  <SortableHead column="cpu" className="w-28" {...list.head}>{t("hosts.columns.cpu")}</SortableHead>
                  <SortableHead column="memory" className="w-28" {...list.head}>{t("hosts.columns.memory")}</SortableHead>
                  <SortableHead column="disk" className="w-28" {...list.head}>{t("hosts.columns.disk")}</SortableHead>
                  <SortableHead column="load" className="hidden lg:table-cell text-right" {...list.head}>{t("hosts.columns.load")}</SortableHead>
                  <SortableHead column="agent" defaultDir="asc" className="hidden md:table-cell" {...list.head}>{t("hosts.columns.agent")}</SortableHead>
                  <SortableHead column="last_seen" {...list.head}>{t("hosts.columns.lastSeen")}</SortableHead>
                  <TableHead className="hidden lg:table-cell">{t("hosts.columns.attributes")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {hosts.map((h) => {
                  const seen = parseTimeParam(h.last_seen) ?? 0;
                  return (
                    <TableRow
                      key={h.host_id}
                      className="cursor-pointer"
                      onClick={(e) => {
                        if ((e.target as HTMLElement).closest("a")) return;
                        void navigate({ to: "/hosts/$hostId", params: { hostId: h.host_id }, search: (prev) => ({ range: prev.range, from: prev.from, to: prev.to }) });
                      }}
                    >
                      <TableCell className="font-medium">
                        <Link
                          to="/hosts/$hostId"
                          params={{ hostId: h.host_id }}
                          search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to })}
                          className="hover:underline"
                          aria-label={t("hosts.open", { name: h.host_name })}
                        >
                          {h.host_name || h.host_id}
                        </Link>
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {h.os_description}
                        <div className="font-mono text-xs opacity-70">{h.arch}</div>
                      </TableCell>
                      <TableCell label={t("hosts.columns.cpu")}>
                        <UsageBar label={t("hosts.columns.cpu")} share={h.usage?.cpu} />
                      </TableCell>
                      <TableCell label={t("hosts.columns.memory")}>
                        <UsageBar label={t("hosts.columns.memory")} share={h.usage?.memory} />
                      </TableCell>
                      <TableCell label={t("hosts.columns.disk")}>
                        <UsageBar label={t("hosts.columns.disk")} share={h.usage?.disk} />
                      </TableCell>
                      <TableCell label={t("hosts.columns.load")} className="hidden lg:table-cell text-right font-mono text-xs tabular-nums">
                        {/* The load per CPU is what makes the number readable without knowing the machine. */}
                        <span title={h.usage?.load_per_cpu != null ? t("hosts.usage.loadPerCpu", { value: formatLoad(h.usage.load_per_cpu, locale) }) : undefined}>
                          {formatLoad(h.usage?.load1, locale)}
                        </span>
                      </TableCell>
                      <TableCell label={t("hosts.columns.agent")} className="hidden font-mono text-xs md:table-cell">
                        {h.agent_version}
                      </TableCell>
                      <TableCell label={t("hosts.columns.lastSeen")} className="whitespace-nowrap">
                        <time dateTime={h.last_seen} title={formatDateTime(seen, locale)}>
                          {formatRelative(seen, now, locale)}
                        </time>
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        <AttributeChips attributes={h.resource_attributes} />
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            <ListPager list={list} count={hosts.length} hasMore={query.data.has_more ?? false} />
          </>
        )}
      </div>
    </div>
  );
}
