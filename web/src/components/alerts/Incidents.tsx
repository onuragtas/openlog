import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { BellRing, CheckCircle2, MessageSquare, Repeat, Send, ShieldAlert, VolumeX, Waves, XCircle } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  acknowledgeIncident,
  addIncidentNote,
  alertIncidentQuery,
  alertIncidentsQuery,
  resolveIncident,
  type AlertIncidentEvent,
  type AlertSeverity,
} from "@/api/alerts";
import { AttributeChips } from "@/components/AttributeChips";
import { DateTimeText, FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDurationShort } from "@/lib/alerts";
import { useNow } from "@/lib/hooks";
import { usePermissions } from "@/lib/org-writable";
import { IncidentStateBadge, SeverityBadge } from "./badges";
import { DeliveriesTable } from "./DeliveriesTable";
import { Section } from "./fields";

const STATE_FILTERS = ["active", "open", "acknowledged", "resolved", "all"] as const;
export type IncidentStateFilter = (typeof STATE_FILTERS)[number];

const parse = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));

function stateParam(f: IncidentStateFilter): string | undefined {
  if (f === "all") return undefined;
  if (f === "active") return "open,acknowledged";
  return f;
}

export function IncidentsList({
  stateFilter,
  severity,
  onFilterChange,
}: {
  stateFilter: IncidentStateFilter;
  severity?: string;
  onFilterChange: (f: { state: IncidentStateFilter; severity?: string }) => void;
}) {
  const { t } = useTranslation();
  const uid = useId();
  const now = useNow(15_000);
  const q = useQuery(alertIncidentsQuery({ state: stateParam(stateFilter), severity }));
  const counts = q.data?.counts;
  const countOf = (f: IncidentStateFilter) =>
    !counts ? undefined : f === "active" ? counts.open + counts.acknowledged : f === "open" || f === "acknowledged" || f === "resolved" ? counts[f] : undefined;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <div role="group" aria-label={t("alerts.incidents.filter")} className="inline-flex flex-wrap gap-1 rounded-lg bg-muted p-1">
          {STATE_FILTERS.map((f) => {
            const c = countOf(f);
            return (
              <button
                key={f}
                type="button"
                aria-pressed={stateFilter === f}
                onClick={() => onFilterChange({ state: f, severity })}
                className="rounded-md px-3 py-1 text-sm text-muted-foreground aria-pressed:bg-background aria-pressed:font-medium aria-pressed:text-foreground aria-pressed:shadow-xs"
              >
                {f === "active" ? t("alerts.incidents.active") : f === "all" ? t("alerts.incidents.all") : t(`alerts.incidentState.${f}`)}
                {c !== undefined && <span className="ml-1.5 font-mono text-xs">{c}</span>}
              </button>
            );
          })}
        </div>
        <label htmlFor={`${uid}-sev`} className="sr-only">
          {t("alerts.incidents.severity")}
        </label>
        <NativeSelect id={`${uid}-sev`} value={severity ?? ""} onChange={(e) => onFilterChange({ state: stateFilter, severity: e.target.value || undefined })}>
          <option value="">{t("alerts.incidents.allSeverities")}</option>
          {(["critical", "warning", "info"] as AlertSeverity[]).map((s) => (
            <option key={s} value={s}>
              {t(`alerts.severity.${s}`)}
            </option>
          ))}
        </NativeSelect>
      </div>
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.data.incidents.length === 0 ? (
        <EmptyState icon={<CheckCircle2 className="size-5 text-success-text" aria-hidden="true" />}>
          {stateFilter === "active" ? t("alerts.incidents.emptyActive") : t("alerts.incidents.empty")}
        </EmptyState>
      ) : (
        <div className="rounded-xl border bg-card">
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("alerts.incidents.columns.state")}</TableHead>
                <TableHead>{t("alerts.incidents.columns.severity")}</TableHead>
                <TableHead>{t("alerts.incidents.columns.incident")}</TableHead>
                <TableHead>{t("alerts.incidents.columns.rule")}</TableHead>
                <TableHead>{t("alerts.incidents.columns.opened")}</TableHead>
                <TableHead>{t("alerts.incidents.columns.duration")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {q.data.incidents.map((inc) => {
                const end = inc.resolved_at ? parse(inc.resolved_at) : now;
                return (
                  <TableRow key={inc.id} data-testid="incident-row">
                    <TableCell className="max-md:w-auto">
                      <div className="flex flex-wrap gap-1">
                        <IncidentStateBadge state={inc.state} />
                        {inc.muted && (
                          <Badge variant="muted">
                            <VolumeX aria-hidden="true" />
                            {t("alerts.incidents.muted")}
                          </Badge>
                        )}
                        {inc.flapping && <Badge variant="outline">{t("alerts.incidents.flapping")}</Badge>}
                      </div>
                    </TableCell>
                    <TableCell className="max-md:w-auto">
                      <SeverityBadge severity={inc.severity} />
                    </TableCell>
                    <TableCell className="max-w-xl">
                      <Link to="/alerts/incidents/$incidentId" params={{ incidentId: inc.id }} className="font-medium hover:underline">
                        {inc.summary || inc.rule_name}
                      </Link>
                    </TableCell>
                    <TableCell label={t("alerts.incidents.columns.rule")} className="whitespace-nowrap">
                      {inc.rule_name}
                    </TableCell>
                    <TableCell label={t("alerts.incidents.columns.opened")} className="whitespace-nowrap">
                      <DateTimeText value={inc.opened_at} relative />
                    </TableCell>
                    <TableCell label={t("alerts.incidents.columns.duration")} className="whitespace-nowrap font-mono text-xs">{formatDurationShort(Math.max(0, (end - parse(inc.opened_at)) / 1000))}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

const EVENT_ICONS: Record<AlertIncidentEvent["kind"], typeof BellRing> = {
  opened: BellRing,
  flapping: Waves,
  acknowledged: ShieldAlert,
  note: MessageSquare,
  renotified: Repeat,
  resolved: CheckCircle2,
  notification_delivered: Send,
  notification_failed: XCircle,
  notification_suppressed: VolumeX,
  notification_muted: VolumeX,
};

function fmtNum(v: number | null): string {
  return v === null ? "–" : String(Math.round(v * 10_000) / 10_000);
}

export function IncidentDetail({ id }: { id: string }) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const canWrite = usePermissions().can("alerts.write");
  const q = useQuery(alertIncidentQuery(id));
  const [resolving, setResolving] = useState(false);
  const [note, setNote] = useState("");
  const [resolveNote, setResolveNote] = useState("");
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["alerts"] });
  const ack = useMutation({ mutationFn: () => acknowledgeIncident(id), onSuccess: refresh });
  const resolve = useMutation({ mutationFn: () => resolveIncident(id, resolveNote), onSuccess: () => { setResolving(false); refresh(); } });
  const addNote = useMutation({ mutationFn: () => addIncidentNote(id, note), onSuccess: () => { setNote(""); refresh(); } });

  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const inc = q.data;
  const labels = Object.fromEntries(Object.entries(inc.labels).filter(([k]) => !k.startsWith("alert.")));

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2 rounded-xl border bg-card p-4">
        <div className="flex flex-wrap items-center gap-2">
          <IncidentStateBadge state={inc.state} />
          <SeverityBadge severity={inc.severity} />
          {inc.muted && <Badge variant="muted">{t("alerts.incidents.muted")}</Badge>}
          {inc.flapping && <Badge variant="outline">{t("alerts.incidents.flapping")}</Badge>}
        </div>
        <h2 className="text-lg font-semibold" data-testid="incident-summary">
          {inc.summary}
        </h2>
        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2 xl:grid-cols-4">
          <div>
            <dt className="text-muted-foreground">{t("alerts.incident.rule")}</dt>
            <dd>
              {inc.rule_id ? (
                <Link to="/alerts/rules/$ruleId" params={{ ruleId: inc.rule_id }} className="font-medium hover:underline">
                  {inc.rule_name}
                </Link>
              ) : (
                <span>
                  {inc.rule_name} <span className="text-muted-foreground">{t("alerts.incident.ruleDeleted")}</span>
                </span>
              )}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("alerts.incident.value")}</dt>
            <dd className="font-mono">
              {fmtNum(inc.value)} <span className="text-muted-foreground">/ {t("alerts.incident.threshold")} {fmtNum(inc.threshold)}</span>
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("alerts.incident.lastValue")}</dt>
            <dd className="font-mono">{fmtNum(inc.last_value)}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("alerts.incident.opened")}</dt>
            <dd>
              <DateTimeText value={inc.opened_at} />
            </dd>
          </div>
          {inc.acknowledged_at && (
            <div>
              <dt className="text-muted-foreground">{t("alerts.incidentState.acknowledged")}</dt>
              <dd>
                {t("alerts.incident.acknowledgedBy", { email: inc.acknowledged_by_email ?? "" })} · <DateTimeText value={inc.acknowledged_at} relative />
              </dd>
            </div>
          )}
          {inc.resolved_at && (
            <div>
              <dt className="text-muted-foreground">{t("alerts.incidentState.resolved")}</dt>
              <dd>
                {inc.resolved_by_email ? t("alerts.incident.resolvedBy", { email: inc.resolved_by_email }) : t("alerts.incident.resolvedAuto")}
                {inc.resolve_reason && ` · ${t(`alerts.resolveReason.${inc.resolve_reason}`)}`} · <DateTimeText value={inc.resolved_at} relative />
              </dd>
            </div>
          )}
        </dl>
        {Object.keys(labels).length > 0 && (
          <div className="flex flex-col gap-1">
            <span className="text-sm text-muted-foreground">{t("alerts.incident.labels")}</span>
            <AttributeChips attributes={labels} />
          </div>
        )}
        {canWrite ? (
          inc.state !== "resolved" && (
            <div className="mt-2 flex flex-wrap items-start gap-2">
              {inc.state === "open" && (
                <Button type="button" variant="outline" disabled={ack.isPending} onClick={() => ack.mutate()}>
                  <ShieldAlert aria-hidden="true" />
                  {t("alerts.incident.acknowledge")}
                </Button>
              )}
              {!resolving ? (
                <Button type="button" onClick={() => setResolving(true)}>
                  <CheckCircle2 aria-hidden="true" />
                  {t("alerts.incident.resolve")}
                </Button>
              ) : (
                <div className="flex w-full flex-wrap items-end gap-2 sm:w-auto">
                  <div className="flex w-full flex-col gap-1 sm:w-auto">
                    <label htmlFor={`${uid}-rnote`} className="text-sm font-medium">
                      {t("alerts.incident.resolveNote")}
                    </label>
                    <input
                      id={`${uid}-rnote`}
                      value={resolveNote}
                      onChange={(e) => setResolveNote(e.target.value)}
                      className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm sm:w-72 pointer-coarse:h-10 pointer-coarse:text-base"
                    />
                  </div>
                  <Button type="button" disabled={resolve.isPending} onClick={() => resolve.mutate()}>
                    {t("alerts.incident.confirmResolve")}
                  </Button>
                  <Button type="button" variant="ghost" onClick={() => setResolving(false)}>
                    {t("alerts.cancel")}
                  </Button>
                </div>
              )}
              <FormError error={ack.error ?? resolve.error} />
            </div>
          )
        ) : (
          <p role="note" className="text-sm text-muted-foreground">
            {t("alerts.incident.readOnly")}
          </p>
        )}
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)]">
        <Section title={t("alerts.incident.timeline")}>
          <ol className="relative flex flex-col gap-3 border-l pl-5" data-testid="incident-timeline">
            {inc.events.map((e) => {
              const Icon = EVENT_ICONS[e.kind] ?? BellRing;
              const bad = e.kind === "opened" || e.kind === "notification_failed";
              return (
                <li key={e.id} className="relative">
                  <span className={`absolute -left-[1.85rem] flex size-6 items-center justify-center rounded-full border bg-card ${bad ? "text-destructive-text" : "text-muted-foreground"}`}>
                    <Icon className="size-3.5" aria-hidden="true" />
                  </span>
                  <div className="flex flex-wrap items-baseline gap-x-2 text-sm">
                    <span className="font-medium">{t(`alerts.incident.events.${e.kind}`)}</span>
                    <span className="text-xs text-muted-foreground">
                      <DateTimeText value={e.at} />
                    </span>
                    {e.actor_email && <span className="text-xs text-muted-foreground">· {e.actor_email}</span>}
                  </div>
                  {e.message && <p className="text-sm text-muted-foreground">{e.message}</p>}
                </li>
              );
            })}
          </ol>
          {canWrite && (
            <form
              className="flex flex-col gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                if (note.trim()) addNote.mutate();
              }}
            >
              <label htmlFor={`${uid}-note`} className="text-sm font-medium">
                {t("alerts.incident.note")}
              </label>
              <textarea
                id={`${uid}-note`}
                rows={2}
                value={note}
                onChange={(e) => setNote(e.target.value)}
                className="rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs"
              />
              <div>
                <Button type="submit" variant="outline" size="sm" disabled={!note.trim() || addNote.isPending}>
                  <MessageSquare aria-hidden="true" />
                  {t("alerts.incident.addNote")}
                </Button>
              </div>
              <FormError error={addNote.error} />
            </form>
          )}
        </Section>
        <Section title={t("alerts.incident.notifications")}>
          <DeliveriesTable deliveries={inc.deliveries} />
        </Section>
      </div>
    </div>
  );
}
