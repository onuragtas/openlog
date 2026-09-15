import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useId, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { apmTracesQuery } from "@/api/apm";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, parseAttrFilter, type ServiceScope } from "@/lib/apm";
import { encodeFilterState } from "@/lib/querybuilder";
import { formatDateTime } from "@/lib/format";
import { parseTimeParam, type RangeSpec } from "@/lib/time";

export interface TraceFilters {
  qtxn?: string;
  qmin?: string;
  qmax?: string;
  qerr?: boolean;
  qattr?: string;
  qsort?: "timestamp" | "duration";
}

const num = (v: string | undefined) => {
  if (!v) return undefined;
  const n = Number(v);
  return Number.isFinite(n) && n >= 0 ? n : undefined;
};

export function TracesTab({ scope, range, filters, onChange }: { scope: ServiceScope; range: RangeSpec; filters: TraceFilters; onChange: (f: TraceFilters) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const ids = { txn: useId(), min: useId(), max: useId(), attr: useId(), sort: useId(), err: useId() };
  const [draft, setDraft] = useState<TraceFilters>(filters);
  const q = useQuery(
    apmTracesQuery(range, {
      service: scope.service,
      namespace: scope.namespace,
      environment: scope.environment,
      transaction: filters.qtxn,
      minDurationMs: num(filters.qmin),
      maxDurationMs: num(filters.qmax),
      errorsOnly: filters.qerr,
      attrs: parseAttrFilter(filters.qattr),
      sort: filters.qsort,
    }),
  );
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onChange({ qtxn: draft.qtxn || undefined, qmin: draft.qmin || undefined, qmax: draft.qmax || undefined, qerr: draft.qerr || undefined, qattr: draft.qattr || undefined, qsort: draft.qsort });
  };
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardContent className="pt-6">
          <form className="grid grid-cols-1 gap-3 md:grid-cols-6" onSubmit={submit} aria-label={t("apm.service.tabs.traces")}>
            <div className="flex flex-col gap-1 md:col-span-2">
              <Label htmlFor={ids.txn}>{t("apm.traces.transaction")}</Label>
              <Input id={ids.txn} value={draft.qtxn ?? ""} onChange={(e) => setDraft({ ...draft, qtxn: e.target.value })} placeholder="GET /orders/{id}" />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.min}>{t("apm.traces.minDuration")}</Label>
              <Input id={ids.min} type="number" min={0} inputMode="decimal" value={draft.qmin ?? ""} onChange={(e) => setDraft({ ...draft, qmin: e.target.value })} />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.max}>{t("apm.traces.maxDuration")}</Label>
              <Input id={ids.max} type="number" min={0} inputMode="decimal" value={draft.qmax ?? ""} onChange={(e) => setDraft({ ...draft, qmax: e.target.value })} />
            </div>
            <div className="flex flex-col gap-1 md:col-span-2">
              <Label htmlFor={ids.attr}>{t("apm.traces.attributes")}</Label>
              <Input id={ids.attr} value={draft.qattr ?? ""} placeholder={t("apm.traces.attributesHint")} onChange={(e) => setDraft({ ...draft, qattr: e.target.value })} />
            </div>
            <div className="flex items-center gap-2">
              <input id={ids.err} type="checkbox" className="size-4 accent-primary" checked={!!draft.qerr} onChange={(e) => setDraft({ ...draft, qerr: e.target.checked })} />
              <Label htmlFor={ids.err}>{t("apm.traces.errorsOnly")}</Label>
            </div>
            <div className="flex items-center gap-2">
              <Label htmlFor={ids.sort}>{t("apm.traces.sort")}</Label>
              <NativeSelect id={ids.sort} value={draft.qsort ?? "timestamp"} onChange={(e) => setDraft({ ...draft, qsort: e.target.value as TraceFilters["qsort"] })}>
                <option value="timestamp">{t("apm.traces.sorts.timestamp")}</option>
                <option value="duration">{t("apm.traces.sorts.duration")}</option>
              </NativeSelect>
            </div>
            <div className="md:col-start-6 md:justify-self-end">
              <Button type="submit">{t("apm.traces.search")}</Button>
            </div>
          </form>
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
          <CardTitle>
            <h2>{t("apm.service.tabs.traces")}</h2>
          </CardTitle>
          <Link
            to="/traces"
            search={{
              range: range.range,
              from: range.from,
              to: range.to,
              f: encodeFilterState({
                filters: [
                  { key: "service.name", op: "=", value: scope.service },
                  { key: "is_entry", op: "=", value: true },
                  ...(filters.qtxn ? [{ key: "transaction.name", op: "=" as const, value: filters.qtxn }] : []),
                  ...(num(filters.qmin) !== undefined ? [{ key: "duration_ms", op: ">=" as const, value: num(filters.qmin)! }] : []),
                  ...(filters.qerr ? [{ key: "error", op: "=" as const, value: true }] : []),
                ],
                groups: [],
                q: "",
              }),
            }}
            className="text-xs text-primary hover:underline"
          >
            {t("explorer.context.openInTraces")}
          </Link>
        </CardHeader>
        <CardContent className="px-0">
          {q.isPending ? (
            <LoadingState />
          ) : q.isError ? (
            <ErrorState error={q.error} onRetry={() => void q.refetch()} />
          ) : q.data.length === 0 ? (
            <EmptyState>{t("apm.traces.empty")}</EmptyState>
          ) : (
            <Table data-testid="trace-results">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("apm.traces.start")}</TableHead>
                  <TableHead>{t("apm.traces.transaction")}</TableHead>
                  <TableHead>{t("apm.traces.status")}</TableHead>
                  <TableHead className="text-right">{t("apm.metrics.duration")}</TableHead>
                  <TableHead>Trace</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {q.data.map((tr) => (
                  <TableRow key={`${tr.trace_id}|${tr.span_id}`}>
                    <TableCell className="whitespace-nowrap text-xs">{formatDateTime(parseTimeParam(tr.timestamp) ?? 0, locale, true)}</TableCell>
                    <TableCell className="max-w-[28rem] truncate font-medium" title={tr.transaction_name}>
                      {tr.transaction_name}
                    </TableCell>
                    <TableCell>
                      {tr.is_error ? <Badge variant="destructive">{tr.http_status_code || t("apm.traces.error")}</Badge> : <Badge variant="muted">{tr.http_status_code || t("apm.traces.ok")}</Badge>}
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{formatMs(tr.duration_ms, locale)}</TableCell>
                    <TableCell>
                      <Link to="/traces/$traceId" params={{ traceId: tr.trace_id }} search={{ span: tr.span_id }} className="font-mono text-xs text-primary hover:underline" aria-label={t("apm.traces.openTrace", { id: tr.trace_id })}>
                        {tr.trace_id.slice(0, 16)}…
                      </Link>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
