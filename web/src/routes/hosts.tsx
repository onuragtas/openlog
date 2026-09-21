import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { hostsQuery } from "@/api/queries";
import type { Host } from "@/api/types";
import { AttributeChips } from "@/components/AttributeChips";
import { PageHeader } from "@/components/AppShell";
import { AddDataLink } from "@/components/onboarding/AddDataLink";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatLoad, UsageBar } from "@/components/hosts/UsageBar";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam } from "@/lib/time";

const route = getRouteApi("/app/hosts");

export function filterHosts(hosts: Host[], q: string): Host[] {
  const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return hosts;
  return hosts.filter((h) => {
    const text = [h.host_name, h.host_id, h.os_description, h.arch, h.agent_version, ...Object.entries(h.resource_attributes).map(([k, v]) => `${k}=${v}`)]
      .join(" ")
      .toLowerCase();
    return terms.every((t) => text.includes(t));
  });
}

export function HostsPage() {
  const { t, i18n } = useTranslation();
  const search = route.useSearch();
  const navigate = useNavigate({ from: "/hosts" });
  const id = useId();
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  const query = useQuery(hostsQuery());
  const q = search.q ?? "";
  const hosts = useMemo(() => filterHosts(query.data ?? [], q), [query.data, q]);

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
        ) : query.data.length === 0 ? (
          <EmptyState>
            <p>{t("hosts.empty")}</p>
            <AddDataLink target="linux" label={t("addData.empty.hosts")} />
          </EmptyState>
        ) : hosts.length === 0 ? (
          <EmptyState>{t("hosts.noMatch", { q })}</EmptyState>
        ) : (
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("hosts.columns.name")}</TableHead>
                <TableHead>{t("hosts.columns.os")}</TableHead>
                <TableHead className="w-28">{t("hosts.columns.cpu")}</TableHead>
                <TableHead className="w-28">{t("hosts.columns.memory")}</TableHead>
                <TableHead className="w-28">{t("hosts.columns.disk")}</TableHead>
                <TableHead className="hidden lg:table-cell text-right">{t("hosts.columns.load")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("hosts.columns.agent")}</TableHead>
                <TableHead>{t("hosts.columns.lastSeen")}</TableHead>
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
        )}
      </div>
    </div>
  );
}
