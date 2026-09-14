import { useInfiniteQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { auditLogQuery, useMe, type AuditEvent, type AuditLogFilters } from "@/api/account";
import { can } from "@/api/roles";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { DateTimeText, SettingsSection } from "./common";

/** Action prefixes offered as filters (label key = prefix without the trailing dot). */
const AUDIT_ACTION_PREFIXES =["org.", "member.", "invitation.", "license_key.", "api_key.", "user.", "session.", "fleet.", "integration_setting.", "update.", "alert"] as const;
type ActionGroup = "org" | "member" | "invitation" | "license_key" | "api_key" | "user" | "session" | "fleet" | "integration_setting" | "update" | "alert";

const PERIOD_DAYS = { d1: 1, d7: 7, d30: 30, d90: 90, all: 0 } as const;
type Period = keyof typeof PERIOD_DAYS;

interface Draft {
  actor: string;
  action: string;
  period: Period;
}

const DEFAULT_DRAFT: Draft = { actor: "", action: "", period: "d30" };

function toFilters(d: Draft, now = Date.now()): AuditLogFilters {
  const days = PERIOD_DAYS[d.period];
  return {
    actor: d.actor.trim() || undefined,
    action: d.action || undefined,
    from: days > 0 ? new Date(now - days * 86_400_000).toISOString() : undefined,
  };
}

function summarize(details: AuditEvent["details"]): string {
  const parts = Object.entries(details ?? {}).map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`);
  const s = parts.join(", ");
  return s.length > 160 ? `${s.slice(0, 160)}…` : s;
}

/** Settings → Audit log (admin+): filter by actor, action and period; newest first with "Load more". */
export function AuditLogSettings({ pageSize = 50 }: { pageSize?: number }) {
  const { t } = useTranslation();
  const id = useId();
  const me = useMe().data;
  const allowed = can(me?.role, "audit.read") && me?.auth === "session";
  const [draft, setDraft] = useState<Draft>(DEFAULT_DRAFT);
  const [filters, setFilters] = useState<AuditLogFilters>(() => toFilters(DEFAULT_DRAFT));
  const log = useInfiniteQuery({ ...auditLogQuery(filters, pageSize), enabled: allowed });

  if (me && !allowed) return <EmptyState>{t("settings.audit.forbidden")}</EmptyState>;
  const events = log.data?.pages.flatMap((p) => p.events) ?? [];

  return (
    <SettingsSection title={t("settings.audit.title")} description={t("settings.audit.description")}>
      <form
        role="search"
        className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-[1fr_auto_auto_auto] lg:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          setFilters(toFilters(draft));
        }}
      >
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-actor`}>{t("settings.audit.actor")}</Label>
          <Input id={`${id}-actor`} type="search" value={draft.actor} placeholder={t("settings.audit.actorPlaceholder")} onChange={(e) => setDraft({ ...draft, actor: e.target.value })} />
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-action`}>{t("settings.audit.action")}</Label>
          <NativeSelect id={`${id}-action`} value={draft.action} onChange={(e) => setDraft({ ...draft, action: e.target.value })}>
            <option value="">{t("settings.audit.actions.all")}</option>
            {AUDIT_ACTION_PREFIXES.map((p) => (
              <option key={p} value={p}>
                {t(`settings.audit.actions.${p.replace(/\.$/, "") as ActionGroup}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-period`}>{t("settings.audit.period")}</Label>
          <NativeSelect id={`${id}-period`} value={draft.period} onChange={(e) => setDraft({ ...draft, period: e.target.value as Period })}>
            {(Object.keys(PERIOD_DAYS) as Period[]).map((p) => (
              <option key={p} value={p}>
                {t(`settings.audit.periods.${p}`)}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex gap-2">
          <Button type="submit">{t("common.apply")}</Button>
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              setDraft(DEFAULT_DRAFT);
              setFilters(toFilters(DEFAULT_DRAFT));
            }}
          >
            {t("settings.audit.reset")}
          </Button>
        </div>
      </form>

      {log.isPending ? (
        <LoadingState />
      ) : log.isError ? (
        <ErrorState error={log.error} onRetry={() => void log.refetch()} />
      ) : events.length === 0 ? (
        <EmptyState>{t("settings.audit.empty")}</EmptyState>
      ) : (
        <>
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("settings.audit.columns.time")}</TableHead>
                <TableHead>{t("settings.audit.columns.action")}</TableHead>
                <TableHead>{t("settings.audit.columns.actor")}</TableHead>
                <TableHead>{t("settings.audit.columns.target")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("settings.audit.columns.details")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.audit.columns.ip")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((ev) => (
                <TableRow key={ev.id}>
                  <TableCell className="whitespace-nowrap max-md:w-auto">
                    <DateTimeText value={ev.created_at} />
                  </TableCell>
                  <TableCell className="max-md:w-auto">
                    <Badge variant="secondary" className="font-mono">
                      {ev.action}
                    </Badge>
                  </TableCell>
                  <TableCell label={t("settings.audit.columns.actor")} className="break-all">
                    {ev.actor_email || <span className="text-muted-foreground">{t("settings.audit.system")}</span>}
                  </TableCell>
                  <TableCell label={t("settings.audit.columns.target")} className="break-all">
                    {ev.target_type && <span className="text-muted-foreground">{ev.target_type} </span>}
                    <span className="font-mono text-xs">{ev.target_id}</span>
                  </TableCell>
                  <TableCell label={t("settings.audit.columns.details")} className="hidden font-mono text-xs break-all text-muted-foreground lg:table-cell">
                    <span title={JSON.stringify(ev.details)}>{summarize(ev.details)}</span>
                  </TableCell>
                  <TableCell label={t("settings.audit.columns.ip")} className="hidden font-mono text-xs md:table-cell">
                    {ev.ip}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {log.hasNextPage && (
            <div className="flex justify-center">
              <Button type="button" variant="outline" disabled={log.isFetchingNextPage} onClick={() => void log.fetchNextPage()}>
                {log.isFetchingNextPage && <Loader2 className="animate-spin" aria-hidden="true" />}
                {t("settings.audit.loadMore")}
              </Button>
            </div>
          )}
        </>
      )}
    </SettingsSection>
  );
}
