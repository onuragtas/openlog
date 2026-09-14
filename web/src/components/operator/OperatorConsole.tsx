import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Flag } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  OPERATOR_PAGE_SIZE,
  operatorFlagsQuery,
  operatorMeQuery,
  operatorOrgsQuery,
  resolveFlag,
  type AbuseFlag,
  type OperatorOrgFilter,
} from "@/api/operator";
import { plansQuery } from "@/api/usage";
import { PageHeader } from "@/components/AppShell";
import { DateTimeText } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatBytes } from "@/lib/format";
import { ReasonAction } from "./ReasonAction";

const STATES = ["active", "trial", "suspended", "flagged"] as const;
const SORTS = ["created", "name", "ingest", "members", "last_ingest"] as const;

export function StateBadge({ state }: { state: string }) {
  const { t } = useTranslation();
  const variant = state === "suspended" ? "destructive" : state === "trial" ? "warning" : "success";
  return <Badge variant={variant}>{t(`operator.states.${state as (typeof STATES)[number]}`)}</Badge>;
}

/** Guards operator pages: only superadmins (GET /api/v1/operator/me). */
export function OperatorGuard({ children }: { children: React.ReactNode }) {
  const { t } = useTranslation();
  const me = useQuery(operatorMeQuery());
  if (me.isPending) return <LoadingState />;
  if (!me.data?.operator) return <EmptyState>{t("operator.notOperator")}</EmptyState>;
  return (
    <>
      {!me.data.saas_mode && (
        <p role="note" className="mb-3 rounded-md border border-warning/60 bg-warning/10 px-3 py-2 text-sm">
          {t("operator.saasModeOff")}
        </p>
      )}
      {children}
    </>
  );
}

export function OperatorConsole() {
  const { t } = useTranslation();
  return (
    <div className="mx-auto w-full max-w-7xl">
      <PageHeader title={t("operator.title")} subtitle={t("operator.subtitle")} />
      <OperatorGuard>
        <Tabs defaultValue="organizations">
          <TabsList>
            <TabsTrigger value="organizations">{t("operator.tabs.organizations")}</TabsTrigger>
            <TabsTrigger value="flagged">{t("operator.tabs.flagged")}</TabsTrigger>
          </TabsList>
          <TabsContent value="organizations" className="mt-4">
            <OrganizationList />
          </TabsContent>
          <TabsContent value="flagged" className="mt-4">
            <FlagList />
          </TabsContent>
        </Tabs>
      </OperatorGuard>
    </div>
  );
}

function OrganizationList() {
  const { t } = useTranslation();
  const id = useId();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<OperatorOrgFilter>({ sort: "created" });
  const plans = useQuery(plansQuery()).data?.plans ?? [];
  const orgs = useQuery(operatorOrgsQuery(filter));
  const set = (patch: Partial<OperatorOrgFilter>) => setFilter((f) => ({ ...f, offset: 0, ...patch }));

  return (
    <div className="flex flex-col gap-3">
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          set({ q: search.trim() });
        }}
      >
        <div className="flex min-w-0 flex-1 flex-col gap-1 sm:min-w-64">
          <label htmlFor={`${id}-q`} className="text-xs text-muted-foreground">
            {t("operator.filters.search")}
          </label>
          <Input id={`${id}-q`} type="search" value={search} placeholder={t("operator.filters.searchPlaceholder")} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor={`${id}-plan`} className="text-xs text-muted-foreground">
            {t("operator.filters.plan")}
          </label>
          <NativeSelect id={`${id}-plan`} value={filter.plan ?? ""} onChange={(e) => set({ plan: e.target.value || undefined })}>
            <option value="">{t("operator.filters.any")}</option>
            {plans.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor={`${id}-state`} className="text-xs text-muted-foreground">
            {t("operator.filters.state")}
          </label>
          <NativeSelect id={`${id}-state`} value={filter.state ?? ""} onChange={(e) => set({ state: (e.target.value || undefined) as OperatorOrgFilter["state"] })}>
            <option value="">{t("operator.filters.any")}</option>
            {STATES.map((s) => (
              <option key={s} value={s}>
                {t(`operator.states.${s}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor={`${id}-sort`} className="text-xs text-muted-foreground">
            {t("operator.filters.sort")}
          </label>
          <NativeSelect id={`${id}-sort`} value={filter.sort} onChange={(e) => set({ sort: e.target.value as OperatorOrgFilter["sort"] })}>
            {SORTS.map((s) => (
              <option key={s} value={s}>
                {t(`operator.sort.${s}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
        <Button type="submit" size="sm">
          {t("operator.filters.search")}
        </Button>
      </form>

      {orgs.isPending ? (
        <LoadingState />
      ) : orgs.isError ? (
        <ErrorState error={orgs.error} onRetry={() => void orgs.refetch()} />
      ) : orgs.data.organizations.length === 0 ? (
        <EmptyState>{t("operator.empty")}</EmptyState>
      ) : (
        <>
          <p className="text-xs text-muted-foreground">{t("operator.total", { count: orgs.data.total })}</p>
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("operator.columns.organization")}</TableHead>
                <TableHead>{t("operator.columns.plan")}</TableHead>
                <TableHead>{t("operator.columns.state")}</TableHead>
                <TableHead className="text-right">{t("operator.columns.members")}</TableHead>
                <TableHead className="text-right">{t("operator.columns.hosts")}</TableHead>
                <TableHead className="text-right">{t("operator.columns.ingest")}</TableHead>
                <TableHead>{t("operator.columns.lastIngest")}</TableHead>
                <TableHead>{t("operator.columns.created")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {orgs.data.organizations.map((o) => (
                <TableRow key={o.id}>
                  <TableCell label={t("operator.columns.organization")}>
                    <Link to="/operator/orgs/$orgId" params={{ orgId: o.id }} className="font-medium underline-offset-4 hover:underline">
                      {o.name}
                    </Link>
                    <div className="font-mono text-xs text-muted-foreground">{o.tenant_id}</div>
                  </TableCell>
                  <TableCell label={t("operator.columns.plan")}>{o.plan_id}</TableCell>
                  <TableCell label={t("operator.columns.state")}>
                    <span className="inline-flex flex-wrap gap-1">
                      <StateBadge state={o.state} />
                      {o.open_flags > 0 && (
                        <Badge variant="warning">
                          <Flag aria-hidden="true" />
                          {o.open_flags}
                        </Badge>
                      )}
                    </span>
                  </TableCell>
                  <TableCell label={t("operator.columns.members")} className="text-right tabular-nums">
                    {o.members}
                  </TableCell>
                  <TableCell label={t("operator.columns.hosts")} className="text-right tabular-nums">
                    {o.active_hosts}
                  </TableCell>
                  <TableCell label={t("operator.columns.ingest")} className="text-right tabular-nums">
                    {formatBytes(o.ingest_bytes)}
                  </TableCell>
                  <TableCell label={t("operator.columns.lastIngest")}>
                    <DateTimeText value={o.last_ingest_at} relative />
                  </TableCell>
                  <TableCell label={t("operator.columns.created")}>
                    <DateTimeText value={o.created_at} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <div className="flex gap-2">
            <Button type="button" size="sm" variant="outline" disabled={!filter.offset} onClick={() => setFilter((f) => ({ ...f, offset: Math.max(0, (f.offset ?? 0) - OPERATOR_PAGE_SIZE) }))}>
              {t("operator.previous")}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={(filter.offset ?? 0) + OPERATOR_PAGE_SIZE >= orgs.data.total}
              onClick={() => setFilter((f) => ({ ...f, offset: (f.offset ?? 0) + OPERATOR_PAGE_SIZE }))}
            >
              {t("operator.next")}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

export function FlagCard({ flag, showOrg = true }: { flag: AbuseFlag; showOrg?: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const resolve = useMutation({
    mutationFn: ({ status, note }: { status: "dismissed" | "actioned"; note: string }) => resolveFlag(flag.id, status, note),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["operator"] }),
  });
  return (
    <li className="flex flex-col gap-2 rounded-lg border bg-card p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{t(`operator.flags.kinds.${flag.kind}`)}</span>
        <Badge variant={flag.status === "open" ? "warning" : "muted"}>{flag.status}</Badge>
        {flag.auto_suspended && <Badge variant="destructive">{t("operator.flags.autoSuspended")}</Badge>}
        {showOrg && (
          <Link to="/operator/orgs/$orgId" params={{ orgId: flag.org_id }} className="underline underline-offset-4">
            {flag.org_name} ({flag.tenant_id})
          </Link>
        )}
      </div>
      <code className="break-all font-mono text-xs text-muted-foreground">{JSON.stringify(flag.details ?? {})}</code>
      <p className="text-xs text-muted-foreground">
        {t("operator.flags.occurrences", { count: flag.occurrences })} · {t("operator.flags.lastSeen")} <DateTimeText value={flag.last_seen_at} relative />
      </p>
      {flag.status === "open" && (
        <div className="flex flex-wrap gap-2">
          <ReasonAction label={t("operator.flags.dismiss")} onConfirm={(note) => resolve.mutateAsync({ status: "dismissed", note })} />
          <ReasonAction label={t("operator.flags.actioned")} onConfirm={(note) => resolve.mutateAsync({ status: "actioned", note })} />
        </div>
      )}
      {flag.resolution_note && <p className="text-xs">{flag.resolution_note}</p>}
    </li>
  );
}

function FlagList() {
  const { t } = useTranslation();
  const flags = useQuery(operatorFlagsQuery("open"));
  if (flags.isPending) return <LoadingState />;
  if (flags.isError) return <ErrorState error={flags.error} onRetry={() => void flags.refetch()} />;
  if (flags.data.length === 0) return <EmptyState>{t("operator.flags.empty")}</EmptyState>;
  return (
    <ul className="flex flex-col gap-2">
      {flags.data.map((f) => (
        <FlagCard key={f.id} flag={f} />
      ))}
    </ul>
  );
}
