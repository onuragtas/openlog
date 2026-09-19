// Error group detail: workflow state and actions, occurrence trend, affected versions/hosts/containers/
// transactions, stack trace, samples, comments and activity (GET …/errors/{group_id}).
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Trash2, X } from "lucide-react";
import { useId, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { addApmErrorComment, apmErrorGroupQuery, deleteApmErrorComment, type ApmAffected, type ApmErrorGroupDetail } from "@/api/apm";
import { Sparkline } from "@/components/apm/Charts";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatMs, stackLines, type ServiceScope } from "@/lib/apm";
import { activityTexts, canDeleteComment, commentProblem, COMMENT_MAX_BYTES } from "@/lib/apm-errors";
import { formatDateTime, formatNumber, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseTimeParam, type RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";
import { StatusBadges } from "./StatusBadges";
import { errorMessage, useErrorWorkflow, useInvalidateErrorWorkflow, WorkflowActions } from "./WorkflowActions";

export interface ErrorGroupPanelProps {
  scope: ServiceScope;
  range: RangeSpec;
  groupId: string;
  onClose: () => void;
  onOpenTransaction?: (name: string) => void;
  defaultVersion?: string;
}

export function ErrorGroupPanel({ scope, range, groupId, onClose, onOpenTransaction, defaultVersion }: ErrorGroupPanelProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const q = useQuery(apmErrorGroupQuery(scope, range, groupId));
  const wf = useErrorWorkflow();
  const d = q.data;

  return (
    <Card data-testid="error-group-detail" className="border-destructive/40">
      <CardHeader className="flex flex-row items-start justify-between gap-2">
        <div className="flex min-w-0 flex-col gap-1">
          <CardTitle>
            <h2 className="font-mono break-all text-destructive-text">{d?.error_type ?? groupId}</h2>
          </CardTitle>
          {d && <p className="text-sm break-words">{d.message}</p>}
          {d && d.workflow && <StatusBadges group={d} />}
        </div>
        <Button variant="ghost" size="icon" aria-label={t("apm.errors.close")} onClick={onClose}>
          <X className="size-4" aria-hidden="true" />
        </Button>
      </CardHeader>
      <CardContent>
        {q.isPending ? (
          <LoadingState />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <div className="flex flex-col gap-4">
            {q.data.workflow && wf.canWrite && (
              <WorkflowActions
                key={`${q.data.group_id}|${q.data.status}|${q.data.assignee?.user_id ?? ""}`}
                label={t("apm.errors.groupActions")}
                groupIds={[q.data.group_id]}
                status={q.data.status}
                assigneeUserId={q.data.assignee?.user_id ?? null}
                members={wf.members}
                defaultVersion={q.data.resolved_in_version || defaultVersion}
              />
            )}
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
              <div className="flex min-w-0 flex-col gap-4">
                <Summary d={q.data} locale={locale} />
                {q.data.last_message && (
                  <div>
                    <p className="text-xs font-semibold text-muted-foreground">{t("apm.errors.lastMessage")}</p>
                    <p className="font-mono text-sm break-words">{q.data.last_message}</p>
                  </div>
                )}
                <div>
                  <p className="mb-1 text-xs font-semibold text-muted-foreground">{t("apm.errors.stacktrace")}</p>
                  {q.data.stacktrace ? (
                    <>
                      <pre className="max-h-96 overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed" data-testid="stacktrace" tabIndex={0}>
                        {stackLines(q.data.stacktrace).map((l, i) => (
                          <span key={i} className={cn("block whitespace-pre", l.frame && !l.inApp && "text-muted-foreground", l.inApp && "font-semibold text-foreground")}>
                            {l.text || " "}
                          </span>
                        ))}
                      </pre>
                      {/* Browser stacks are stored minified; uploaded source maps resolve them at read time
                          (rum.md §8). The original stays one click away, because a partly resolved stack is
                          still read against it. */}
                      {q.data.symbolicated_frames > 0 && (
                        <details className="mt-2" data-testid="stacktrace-minified">
                          <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
                            {t("apm.errors.symbolicated", { n: q.data.symbolicated_frames })}
                          </summary>
                          <pre className="mt-1 max-h-96 overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed" tabIndex={0}>
                            {q.data.stacktrace_minified}
                          </pre>
                        </details>
                      )}
                    </>
                  ) : (
                    <p className="text-sm text-muted-foreground">{t("apm.errors.noStack")}</p>
                  )}
                </div>
                <Affected affected={q.data.affected} onOpenTransaction={onOpenTransaction} />
              </div>
              <div className="flex min-w-0 flex-col gap-4">
                <Samples d={q.data} locale={locale} />
                {q.data.workflow && <Comments d={q.data} />}
                {q.data.workflow && <Activity d={q.data} members={wf.members} />}
              </div>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Summary({ d, locale }: { d: ApmErrorGroupDetail; locale: string }) {
  const { t } = useTranslation();
  const at = (s: string | null) => (s ? formatDateTime(parseTimeParam(s) ?? 0, locale) : "–");
  const rows: [string, React.ReactNode][] = [
    [t("apm.errors.count"), formatNumber(d.count, locale)],
    [t("apm.errors.total"), formatNumber(d.total_count, locale)],
    [t("apm.errors.firstSeen"), at(d.first_seen)],
    [t("apm.errors.lastSeen"), at(d.last_seen)],
  ];
  if (d.workflow) {
    rows.push([t("apm.errors.assignee"), d.assignee ? d.assignee.name || d.assignee.email : t("apm.errors.unassigned")]);
    if (d.status === "resolved") {
      rows.push([t("apm.errors.resolvedAt"), `${at(d.resolved_at)}${d.resolved_by_email ? ` · ${d.resolved_by_email}` : ""}`]);
    }
    if (d.resolved_in_version) rows.push([t("apm.errors.resolvedInVersionLabel"), <span className="font-mono">{d.resolved_in_version}</span>]);
    if (d.regression_count > 0) rows.push([t("apm.errors.regressedAt"), `${at(d.regressed_at)} · ${t("apm.errors.regressionCount", { count: d.regression_count })}`]);
  }
  return (
    <div className="flex flex-col gap-2">
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs sm:grid-cols-[auto_minmax(0,1fr)_auto_minmax(0,1fr)]" data-testid="error-group-summary">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-muted-foreground">{k}</dt>
            <dd className="min-w-0 break-words">{v}</dd>
          </div>
        ))}
      </dl>
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <span>{t("apm.errors.trend")}</span>
        <Sparkline points={d.series} label={t("apm.errors.trend")} width={200} />
      </div>
    </div>
  );
}

function AffectedList({ title, items, render }: { title: string; items: ApmAffected[]; render: (a: ApmAffected) => React.ReactNode }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const now = useNow();
  return (
    <div className="min-w-0">
      <h4 className="mb-1 text-xs font-semibold text-muted-foreground">{title}</h4>
      {items.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("apm.errors.noAffected")}</p>
      ) : (
        <ul className="flex flex-col gap-0.5 text-xs">
          {items.map((a) => {
            const last = parseTimeParam(a.last_seen);
            return (
              <li key={a.value} className="flex min-w-0 items-baseline justify-between gap-2">
                <span className="min-w-0 truncate">{render(a)}</span>
                <span className="shrink-0 font-mono tabular-nums text-muted-foreground" title={last !== null ? formatDateTime(last, locale) : undefined}>
                  {formatNumber(a.count, locale)}
                  {last !== null && ` · ${formatRelative(last, now, locale)}`}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function Affected({ affected, onOpenTransaction }: { affected: ApmErrorGroupDetail["affected"]; onOpenTransaction?: (name: string) => void }) {
  const { t } = useTranslation();
  const linkCls = "text-primary hover:underline";
  return (
    <section aria-label={t("apm.errors.affected")} className="flex flex-col gap-2" data-testid="error-affected">
      <h3 className="text-sm font-semibold">{t("apm.errors.affected")}</h3>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <AffectedList title={t("apm.errors.affectedVersions")} items={affected.versions} render={(a) => <span className="font-mono">{a.value || "–"}</span>} />
        <AffectedList
          title={t("apm.errors.affectedHosts")}
          items={affected.hosts}
          render={(a) => (
            <Link to="/hosts/$hostId" params={{ hostId: a.value }} className={cn("font-mono", linkCls)} aria-label={t("apm.service.openHost", { name: a.name || a.value })}>
              {a.name || a.value}
            </Link>
          )}
        />
        <AffectedList
          title={t("apm.errors.affectedContainers")}
          items={affected.containers}
          render={(a) => (
            <Link to="/containers/$containerId" params={{ containerId: a.value }} className={cn("font-mono", linkCls)} aria-label={t("apm.service.openContainer", { name: a.name || a.value.slice(0, 12) })}>
              {a.name || a.value.slice(0, 12)}
            </Link>
          )}
        />
        <AffectedList
          title={t("apm.errors.affectedTransactions")}
          items={affected.transactions}
          render={(a) =>
            onOpenTransaction ? (
              <button type="button" className={cn("text-left", linkCls)} onClick={() => onOpenTransaction(a.value)} aria-label={t("apm.transactions.open", { name: a.value })}>
                {a.value}
              </button>
            ) : (
              a.value
            )
          }
        />
      </div>
    </section>
  );
}

function Samples({ d, locale }: { d: ApmErrorGroupDetail; locale: string }) {
  const { t } = useTranslation();
  return (
    <div className="min-w-0">
      <p className="mb-1 text-xs font-semibold text-muted-foreground">{t("apm.errors.samples")}</p>
      {d.samples.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("apm.errors.noSamples")}</p>
      ) : (
        <ul className="flex flex-col divide-y text-sm" data-testid="error-samples">
          {d.samples.map((s) => (
            <li key={s.span_id} className="flex flex-col gap-0.5 py-2">
              <div className="flex items-center justify-between gap-2">
                <Link to="/traces/$traceId" params={{ traceId: s.trace_id }} search={{ span: s.span_id }} className="font-mono text-xs text-primary hover:underline" aria-label={t("apm.traces.openTrace", { id: s.trace_id })}>
                  {s.trace_id.slice(0, 16)}…
                </Link>
                <span className="font-mono text-xs tabular-nums">{formatMs(s.duration_ms, locale)}</span>
              </div>
              <span className="text-xs text-muted-foreground">
                {formatDateTime(parseTimeParam(s.timestamp) ?? 0, locale)} · {s.transaction_name || s.span_name}
                {s.version && <> · {t("apm.errors.sampleVersion", { version: s.version })}</>}
              </span>
              {s.message && (
                <span className="truncate text-xs" title={s.message}>
                  {s.message}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Comments({ d }: { d: ApmErrorGroupDetail }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const wf = useErrorWorkflow();
  const invalidate = useInvalidateErrorWorkflow();
  const [body, setBody] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const add = useMutation({
    mutationFn: (text: string) => addApmErrorComment(d.group_id, text),
    onSuccess: () => {
      setBody("");
      void invalidate();
    },
  });
  const remove = useMutation({ mutationFn: (commentId: string) => deleteApmErrorComment(d.group_id, commentId), onSuccess: () => void invalidate() });
  const bytes = new TextEncoder().encode(body).length;
  const meInfo = wf.me ? { auth: wf.me.auth, role: wf.me.role, userId: wf.userId } : undefined;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const p = commentProblem(body);
    if (p) {
      setProblem(p === "required" ? t("apm.errors.commentRequired") : t("apm.errors.commentTooLong"));
      return;
    }
    setProblem(null);
    add.mutate(body);
  };

  return (
    <section aria-labelledby={`${id}-title`} className="flex flex-col gap-2" data-testid="error-comments">
      <h3 id={`${id}-title`} className="text-sm font-semibold">
        {t("apm.errors.commentsTitle")}
      </h3>
      {d.comments.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("apm.errors.noComments")}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {d.comments.map((c) => (
            <li key={c.id} className="rounded-md border p-2 text-sm">
              <div className="mb-1 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                <span className="min-w-0 truncate">
                  <span className="font-medium text-foreground">{c.author_name || c.author_email}</span> · <time dateTime={c.created_at}>{formatDateTime(parseTimeParam(c.created_at) ?? 0, locale)}</time>
                </span>
                {canDeleteComment(meInfo, c) && (
                  <Button variant="ghost" size="icon" className="size-7" disabled={remove.isPending} aria-label={t("apm.errors.deleteComment", { author: c.author_name || c.author_email })} onClick={() => remove.mutate(c.id)}>
                    <Trash2 className="size-3.5" aria-hidden="true" />
                  </Button>
                )}
              </div>
              <p className="break-words whitespace-pre-wrap">{c.body}</p>
            </li>
          ))}
        </ul>
      )}
      {remove.error && (
        <p role="alert" className="text-xs text-destructive-text">
          {errorMessage(remove.error)}
        </p>
      )}
      {wf.canWrite && (
        <form onSubmit={submit} noValidate className="flex flex-col gap-1.5">
          <label htmlFor={`${id}-body`} className="text-xs font-medium">
            {t("apm.errors.addComment")}
          </label>
          <textarea
            id={`${id}-body`}
            rows={3}
            value={body}
            onChange={(e) => setBody(e.target.value)}
            aria-invalid={!!problem || bytes > COMMENT_MAX_BYTES}
            aria-describedby={`${id}-bytes`}
            className="rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs"
          />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span id={`${id}-bytes`} className={cn("text-xs text-muted-foreground tabular-nums", bytes > COMMENT_MAX_BYTES && "text-destructive-text")}>
              {t("apm.errors.commentBytes", { count: bytes, max: COMMENT_MAX_BYTES })}
            </span>
            <Button type="submit" size="sm" disabled={add.isPending}>
              {t("apm.errors.commentSubmit")}
            </Button>
          </div>
          {(problem || add.error) && (
            <p role="alert" className="text-xs text-destructive-text">
              {problem ?? errorMessage(add.error)}
            </p>
          )}
        </form>
      )}
    </section>
  );
}

function Activity({ d, members }: { d: ApmErrorGroupDetail; members?: { user_id: string; name: string; email: string }[] }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const nameOf = (userId: string) => {
    const m = members?.find((x) => x.user_id === userId);
    return m ? m.name || m.email : userId;
  };
  const entries = [...d.activity].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  return (
    <section aria-label={t("apm.errors.activityTitle")} className="flex flex-col gap-2" data-testid="error-activity">
      <h3 className="text-sm font-semibold">{t("apm.errors.activityTitle")}</h3>
      {entries.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("apm.errors.noActivity")}</p>
      ) : (
        <ol className="flex flex-col gap-1.5 border-l pl-3 text-xs">
          {entries.map((a, i) => (
            <li key={i} className={cn("relative", a.action === "apm.error_group.regressed" && "text-warning-text font-medium")}>
              <span aria-hidden="true" className="absolute top-1.5 -left-[17px] size-2 rounded-full bg-border" />
              <p className="break-words">
                {activityTexts(a, nameOf)
                  // Keys come from activityTexts (lib/apm-errors.ts); all exist under apm.errors.activity.
                  .map((x) => (t as unknown as (key: string, params: Record<string, string | number>) => string)(`apm.errors.activity.${x.key}`, x.params))
                  .join(" · ")}
              </p>
              <time dateTime={a.created_at} className="text-muted-foreground">
                {formatDateTime(parseTimeParam(a.created_at) ?? 0, locale)}
              </time>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
