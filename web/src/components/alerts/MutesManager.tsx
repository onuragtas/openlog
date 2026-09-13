import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { alertMutesQuery, alertRulesQuery, createAlertMute, deleteAlertMute, updateAlertMute, type AlertMute, type AlertMuteMatcher, type AlertRule } from "@/api/alerts";
import { can } from "@/api/roles";
import { ConfirmAction } from "@/components/fleet/ConfirmAction";
import { DateTimeText, FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { canEditOwned } from "@/lib/alerts";
import { useNow } from "@/lib/hooks";
import { fromDateTimeLocal, toDateTimeLocal } from "@/lib/time";
import { Field } from "./fields";

const parse = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));

function MuteForm({ mute, rules, onDone }: { mute: AlertMute | null; rules: AlertRule[]; onDone: () => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const [now] = useState(() => Date.now());
  const [name, setName] = useState(mute?.name ?? "");
  const [comment, setComment] = useState(mute?.comment ?? "");
  const [starts, setStarts] = useState(toDateTimeLocal(mute ? parse(mute.starts_at) : now));
  const [ends, setEnds] = useState(toDateTimeLocal(mute ? parse(mute.ends_at) : now + 2 * 3_600_000));
  const [ruleIds, setRuleIds] = useState<string[]>(mute?.rule_ids ?? []);
  const [matchers, setMatchers] = useState<AlertMuteMatcher[]>(mute?.matchers ?? []);
  const id = (n: string) => `${uid}-${n}`;
  const save = useMutation({
    mutationFn: () => {
      const s = fromDateTimeLocal(starts);
      const e = fromDateTimeLocal(ends);
      const input = {
        name: name.trim(),
        comment,
        starts_at: s === null ? starts : new Date(s).toISOString(),
        ends_at: e === null ? ends : new Date(e).toISOString(),
        rule_ids: ruleIds,
        matchers: matchers.filter((m) => m.label.trim()),
      };
      return mute ? updateAlertMute(mute.id, input) : createAlertMute(input);
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      onDone();
    },
  });
  return (
    <form
      className="flex flex-col gap-4 rounded-xl border bg-card p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Field id={id("name")} label={t("alerts.mutes.name")}>
          <Input id={id("name")} required value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field id={id("comment")} label={t("alerts.mutes.comment")}>
          <Input id={id("comment")} value={comment} onChange={(e) => setComment(e.target.value)} />
        </Field>
        <Field id={id("starts")} label={t("alerts.mutes.starts")}>
          <Input id={id("starts")} type="datetime-local" value={starts} onChange={(e) => setStarts(e.target.value)} />
        </Field>
        <Field id={id("ends")} label={t("alerts.mutes.ends")}>
          <Input id={id("ends")} type="datetime-local" value={ends} onChange={(e) => setEnds(e.target.value)} />
        </Field>
      </div>
      <fieldset>
        <legend className="mb-2 text-sm font-medium">
          {t("alerts.mutes.rules")} <span className="font-normal text-muted-foreground">({t("alerts.mutes.allRules")} = —)</span>
        </legend>
        <div className="flex flex-wrap gap-2">
          {rules.map((r) => (
            <label key={r.id} className="flex items-center gap-2 rounded-md border px-2 py-1 text-sm has-checked:border-primary">
              <input type="checkbox" checked={ruleIds.includes(r.id)} onChange={(e) => setRuleIds(e.target.checked ? [...ruleIds, r.id] : ruleIds.filter((x) => x !== r.id))} />
              {r.name}
            </label>
          ))}
        </div>
      </fieldset>
      <div className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <span className="text-sm font-medium">{t("alerts.mutes.matchers")}</span>
          <Button type="button" variant="outline" size="sm" onClick={() => setMatchers([...matchers, { label: "", op: "eq", value: "" }])}>
            <Plus aria-hidden="true" />
            {t("alerts.mutes.addMatcher")}
          </Button>
        </div>
        {matchers.map((m, i) => {
          const set = (patch: Partial<AlertMuteMatcher>) => setMatchers(matchers.map((x, j) => (j === i ? { ...x, ...patch } : x)));
          return (
            <div key={i} className="flex flex-wrap items-center gap-2">
              <Input aria-label={t("alerts.mutes.matcherLabel", { n: i + 1 })} className="w-48" placeholder="host.name" value={m.label} onChange={(e) => set({ label: e.target.value })} />
              <NativeSelect aria-label={t("alerts.mutes.matcherOp", { n: i + 1 })} value={m.op} onChange={(e) => set({ op: e.target.value as AlertMuteMatcher["op"] })}>
                {(["eq", "neq", "contains"] as const).map((o) => (
                  <option key={o} value={o}>
                    {t(`alerts.mutes.ops.${o}`)}
                  </option>
                ))}
              </NativeSelect>
              <Input aria-label={t("alerts.mutes.matcherValue", { n: i + 1 })} className="w-48" value={m.value} onChange={(e) => set({ value: e.target.value })} />
              <Button type="button" variant="ghost" size="icon" aria-label={t("alerts.mutes.removeMatcher", { n: i + 1 })} onClick={() => setMatchers(matchers.filter((_, j) => j !== i))}>
                <Trash2 aria-hidden="true" />
              </Button>
            </div>
          );
        })}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={save.isPending}>
          {mute ? t("alerts.mutes.save") : t("alerts.mutes.create")}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          {t("alerts.cancel")}
        </Button>
        <FormError error={save.error} />
      </div>
    </form>
  );
}

export function MutesManager() {
  const { t } = useTranslation();
  const me = useMe().data;
  const now = useNow(30_000);
  const canWrite = can(me?.role, "alerts.write");
  const mutes = useQuery(alertMutesQuery());
  const rules = useQuery(alertRulesQuery());
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<AlertMute | "new" | null>(null);
  const remove = useMutation({ mutationFn: (id: string) => deleteAlertMute(id), onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["alerts"] }) });
  const ruleName = new Map((rules.data ?? []).map((r) => [r.id, r.name] as const));
  return (
    <div className="flex flex-col gap-3">
      {!canWrite && (
        <p role="note" className="text-sm text-muted-foreground">
          {t("alerts.mutes.readOnly")}
        </p>
      )}
      {canWrite && editing === null && (
        <div>
          <Button type="button" onClick={() => setEditing("new")}>
            <Plus aria-hidden="true" />
            {t("alerts.mutes.new")}
          </Button>
        </div>
      )}
      {editing !== null && <MuteForm key={editing === "new" ? "new" : editing.id} mute={editing === "new" ? null : editing} rules={rules.data ?? []} onDone={() => setEditing(null)} />}
      <FormError error={remove.error} />
      {mutes.isPending ? (
        <LoadingState />
      ) : mutes.isError ? (
        <ErrorState error={mutes.error} onRetry={() => void mutes.refetch()} />
      ) : mutes.data.length === 0 ? (
        <EmptyState>{t("alerts.mutes.empty")}</EmptyState>
      ) : (
        <div className="rounded-xl border bg-card">
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("alerts.mutes.columns.name")}</TableHead>
                <TableHead>{t("alerts.mutes.columns.window")}</TableHead>
                <TableHead>{t("alerts.mutes.columns.scope")}</TableHead>
                <TableHead>{t("alerts.mutes.columns.status")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("alerts.mutes.edit")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {mutes.data.map((m) => {
                const s = parse(m.starts_at);
                const e = parse(m.ends_at);
                const status = now < s ? "scheduled" : now < e ? "active" : "ended";
                const scope = [
                  m.rule_ids.length ? m.rule_ids.map((id) => ruleName.get(id) ?? id).join(", ") : t("alerts.mutes.allRules"),
                  ...m.matchers.map((x) => `${x.label} ${t(`alerts.mutes.ops.${x.op}`)} ${x.value}`),
                ];
                return (
                  <TableRow key={m.id} data-testid="mute-row">
                    <TableCell>
                      <span className="font-medium">{m.name}</span>
                      {m.comment && <p className="text-xs text-muted-foreground">{m.comment}</p>}
                    </TableCell>
                    <TableCell label={t("alerts.mutes.columns.window")} className="whitespace-nowrap text-sm">
                      <DateTimeText value={m.starts_at} /> – <DateTimeText value={m.ends_at} />
                    </TableCell>
                    <TableCell label={t("alerts.mutes.columns.scope")} className="text-sm">
                      {scope.join(" · ")}
                    </TableCell>
                    <TableCell className="max-md:w-auto">
                      <Badge variant={status === "active" ? "warning" : status === "scheduled" ? "secondary" : "muted"}>{t(`alerts.mutes.${status}`)}</Badge>
                    </TableCell>
                    <TableCell>
                      {canEditOwned(me?.role, m.created_by_user_id, me?.user?.id) && (
                        <div className="flex flex-wrap justify-end gap-2 max-md:justify-start">
                          <Button type="button" variant="outline" size="sm" onClick={() => setEditing(m)}>
                            {t("alerts.mutes.edit")}
                          </Button>
                          <ConfirmAction label={t("alerts.mutes.delete")} confirmLabel={t("alerts.mutes.confirmDelete")} destructive onConfirm={() => remove.mutate(m.id)} />
                        </div>
                      )}
                    </TableCell>
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
