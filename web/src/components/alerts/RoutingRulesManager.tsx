import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  alertChannelsQuery,
  alertRoutingRulesQuery,
  createRoutingRule,
  deleteRoutingRule,
  reorderRoutingRules,
  updateRoutingRule,
  type AlertChannel,
  type AlertRouteMatcher,
  type AlertRoutingRule,
  type AlertRoutingRuleInput,
  type AlertRuleType,
  type AlertSeverity,
} from "@/api/alerts";
import { ConfirmAction } from "@/components/fleet/ConfirmAction";
import { FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { WEEK_DAYS } from "@/lib/mute-schedule";
import { usePermissions } from "@/lib/org-writable";
import { ChannelTypeLabel } from "./badges";
import { Field } from "./fields";

const SEVERITIES = ["critical", "warning", "info"] as const;
const RULE_TYPES = ["metric_threshold", "log_match", "no_data", "discovery", "apm", "apm_no_data", "apm_error", "oql", "slo_burn", "anomaly"] as const;
const OPS = ["eq", "neq", "contains"] as const;

function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

const toggle = <T,>(list: T[], v: T, on: boolean): T[] => (on ? [...list, v] : list.filter((x) => x !== v));

/** Checkbox list of values (severities, rule types, days, channels). */
function CheckboxGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div role="group" aria-label={label} className="flex min-w-0 flex-col gap-1.5">
      <span className="text-sm font-medium">{label}</span>
      <div className="flex flex-wrap gap-2">{children}</div>
    </div>
  );
}

function CheckboxItem({ checked, onChange, children }: { checked: boolean; onChange: (on: boolean) => void; children: React.ReactNode }) {
  return (
    <label className="flex min-h-10 items-center gap-2 rounded-md border px-2 py-1 text-sm has-checked:border-primary">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {children}
    </label>
  );
}

function RoutingRuleForm({ rule, channels, nextPosition, onDone }: { rule: AlertRoutingRule | null; channels: AlertChannel[]; nextPosition: number; onDone: () => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const [name, setName] = useState(rule?.name ?? "");
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [isDefault, setIsDefault] = useState(rule?.is_default ?? false);
  const [channelIds, setChannelIds] = useState<string[]>(rule?.channel_ids ?? []);
  const [severities, setSeverities] = useState<AlertSeverity[]>((rule?.match?.severities ?? []) as AlertSeverity[]);
  const [ruleTypes, setRuleTypes] = useState<AlertRuleType[]>((rule?.match?.rule_types ?? []) as AlertRuleType[]);
  const [services, setServices] = useState((rule?.match?.services ?? []).join(", "));
  const [labels, setLabels] = useState<AlertRouteMatcher[]>(rule?.match?.labels ?? []);
  const window0 = rule?.match?.time_window ?? null;
  const [windowOn, setWindowOn] = useState(!!window0);
  const [timezone, setTimezone] = useState(window0?.timezone ?? browserTimeZone());
  const [days, setDays] = useState<string[]>(window0?.days ?? ["mon", "tue", "wed", "thu", "fri"]);
  const [startTime, setStartTime] = useState(window0?.start_time ?? "09:00");
  const [endTime, setEndTime] = useState(window0?.end_time ?? "18:00");
  const id = (n: string) => `${uid}-${n}`;

  const save = useMutation({
    mutationFn: () => {
      const input: AlertRoutingRuleInput = {
        name: name.trim(),
        enabled,
        is_default: isDefault,
        // A new rule is appended: it must not jump ahead of the existing ones.
        position: rule?.position ?? nextPosition,
        channel_ids: channelIds,
        // The default route matches every incident, so it carries no conditions.
        match: isDefault
          ? {}
          : {
              severities,
              rule_types: ruleTypes,
              services: services.split(",").map((s) => s.trim()).filter(Boolean),
              labels: labels.filter((l) => l.label.trim()),
              time_window: windowOn ? { timezone: timezone.trim(), days: days as never, start_time: startTime, end_time: endTime } : null,
            },
      };
      return rule ? updateRoutingRule(rule.id, input) : createRoutingRule(input);
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      onDone();
    },
  });

  return (
    <form
      className="flex flex-col gap-4 rounded-xl border bg-card p-4"
      data-testid="routing-rule-form"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field id={id("name")} label={t("alerts.routing.name")}>
          <Input id={id("name")} required value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <div className="flex flex-wrap items-end gap-4 pb-2">
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            {t("alerts.routing.enabled")}
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={isDefault} onChange={(e) => setIsDefault(e.target.checked)} />
            {t("alerts.routing.isDefault")}
          </label>
        </div>
      </div>

      <CheckboxGroup label={t("alerts.routing.channels")}>
        {channels.map((c) => (
          <CheckboxItem key={c.id} checked={channelIds.includes(c.id)} onChange={(on) => setChannelIds(toggle(channelIds, c.id, on))}>
            <ChannelTypeLabel type={c.type} />
            {c.name}
          </CheckboxItem>
        ))}
      </CheckboxGroup>
      {channels.length === 0 && <p className="text-xs text-muted-foreground">{t("alerts.routing.channelsHint")}</p>}

      {!isDefault && (
        <fieldset className="flex min-w-0 flex-col gap-4 rounded-lg border p-3">
          <legend className="px-1 text-sm font-medium">{t("alerts.routing.conditions")}</legend>
          <CheckboxGroup label={t("alerts.routing.severities")}>
            {SEVERITIES.map((s) => (
              <CheckboxItem key={s} checked={severities.includes(s)} onChange={(on) => setSeverities(toggle(severities, s, on))}>
                {t(`alerts.severity.${s}`)}
              </CheckboxItem>
            ))}
          </CheckboxGroup>
          <CheckboxGroup label={t("alerts.routing.ruleTypes")}>
            {RULE_TYPES.map((rt) => (
              <CheckboxItem key={rt} checked={ruleTypes.includes(rt)} onChange={(on) => setRuleTypes(toggle(ruleTypes, rt, on))}>
                {t(`alerts.types.${rt}`)}
              </CheckboxItem>
            ))}
          </CheckboxGroup>
          <Field id={id("services")} label={t("alerts.routing.services")} hint={t("alerts.routing.servicesHint")}>
            <Input id={id("services")} value={services} placeholder="checkout, cart" onChange={(e) => setServices(e.target.value)} />
          </Field>
          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">{t("alerts.routing.labels")}</span>
              <Button type="button" variant="outline" size="sm" onClick={() => setLabels([...labels, { label: "", op: "eq", value: "" }])}>
                <Plus aria-hidden="true" />
                {t("alerts.routing.addLabel")}
              </Button>
            </div>
            {labels.map((m, i) => {
              const set = (patch: Partial<AlertRouteMatcher>) => setLabels(labels.map((x, j) => (j === i ? { ...x, ...patch } : x)));
              return (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Input aria-label={t("alerts.routing.labelName", { n: i + 1 })} className="w-48" placeholder="env" value={m.label} onChange={(e) => set({ label: e.target.value })} />
                  <NativeSelect aria-label={t("alerts.routing.labelOp", { n: i + 1 })} value={m.op} onChange={(e) => set({ op: e.target.value as AlertRouteMatcher["op"] })}>
                    {OPS.map((o) => (
                      <option key={o} value={o}>
                        {t(`alerts.routing.ops.${o}`)}
                      </option>
                    ))}
                  </NativeSelect>
                  <Input aria-label={t("alerts.routing.labelValue", { n: i + 1 })} className="w-48" value={m.value} onChange={(e) => set({ value: e.target.value })} />
                  <Button type="button" variant="ghost" size="icon" aria-label={t("alerts.routing.removeLabel", { n: i + 1 })} onClick={() => setLabels(labels.filter((_, j) => j !== i))}>
                    <Trash2 aria-hidden="true" />
                  </Button>
                </div>
              );
            })}
          </div>
          <div className="flex flex-col gap-3">
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={windowOn} onChange={(e) => setWindowOn(e.target.checked)} />
              {t("alerts.routing.timeWindowToggle")}
            </label>
            {windowOn && (
              <>
                <div className="grid gap-4 sm:grid-cols-3">
                  <Field id={id("tz")} label={t("alerts.routing.timezone")}>
                    <Input id={id("tz")} value={timezone} placeholder="Europe/Istanbul" onChange={(e) => setTimezone(e.target.value)} />
                  </Field>
                  <Field id={id("stime")} label={t("alerts.routing.startTime")}>
                    <Input id={id("stime")} type="time" value={startTime} onChange={(e) => setStartTime(e.target.value)} />
                  </Field>
                  <Field id={id("etime")} label={t("alerts.routing.endTime")} hint={t("alerts.routing.endTimeHint")}>
                    <Input id={id("etime")} type="time" value={endTime} onChange={(e) => setEndTime(e.target.value)} />
                  </Field>
                </div>
                <CheckboxGroup label={t("alerts.routing.days")}>
                  {WEEK_DAYS.map((d) => (
                    <CheckboxItem key={d} checked={days.includes(d)} onChange={(on) => setDays(toggle(days, d as string, on))}>
                      {t(`alerts.mutes.dayNames.${d}`)}
                    </CheckboxItem>
                  ))}
                </CheckboxGroup>
              </>
            )}
          </div>
        </fieldset>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={save.isPending}>
          {rule ? t("alerts.routing.save") : t("alerts.routing.create")}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          {t("alerts.cancel")}
        </Button>
        <FormError error={save.error} />
      </div>
    </form>
  );
}

/** Human summary of what a route matches. */
function useMatchSummary() {
  const { t } = useTranslation();
  return (r: AlertRoutingRule): string[] => {
    if (r.is_default) return [t("alerts.routing.matchAll")];
    const parts: string[] = [];
    const m = r.match ?? {};
    if (m.severities?.length) parts.push(m.severities.map((s) => t(`alerts.severity.${s}`)).join(" / "));
    if (m.rule_types?.length) parts.push(m.rule_types.map((x) => t(`alerts.types.${x}`)).join(", "));
    if (m.services?.length) parts.push(m.services.join(", "));
    for (const l of m.labels ?? []) parts.push(`${l.label} ${t(`alerts.routing.ops.${l.op}`)} ${l.value}`);
    const w = m.time_window;
    if (w) parts.push(`${(w.days ?? []).map((d) => t(`alerts.mutes.dayNames.${d}`)).join(", ")} ${w.start_time}–${w.end_time} (${w.timezone})`);
    return parts.length > 0 ? parts : [t("alerts.routing.matchAll")];
  };
}

/** Routing rules: which channels an incident reaches (alerting.md §5.6). */
export function RoutingRulesManager() {
  const { t } = useTranslation();
  const canManage = usePermissions().can("alerts.manage");
  const queryClient = useQueryClient();
  const rules = useQuery(alertRoutingRulesQuery());
  const channels = useQuery(alertChannelsQuery());
  const [editing, setEditing] = useState<AlertRoutingRule | "new" | null>(null);
  const summary = useMatchSummary();
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["alerts"] });
  const remove = useMutation({ mutationFn: (id: string) => deleteRoutingRule(id), onSuccess: invalidate });
  const reorder = useMutation({ mutationFn: (ids: string[]) => reorderRoutingRules(ids), onSuccess: invalidate });
  const channelName = new Map((channels.data?.channels ?? []).map((c) => [c.id, c.name] as const));

  // Display order = evaluation order: the default route is evaluated last, whatever its stored position (§5.6).
  const ordered = [...(rules.data ?? [])].sort((a, b) => Number(a.is_default) - Number(b.is_default) || a.position - b.position);
  const movable = ordered.filter((r) => !r.is_default).length;

  const move = (index: number, delta: number) => {
    const ids = ordered.map((r) => r.id);
    const next = index + delta;
    if (next < 0 || next >= movable) return;
    [ids[index], ids[next]] = [ids[next]!, ids[index]!];
    reorder.mutate(ids);
  };

  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">{t("alerts.routing.description_")}</p>
      {!canManage && (
        <p role="note" className="text-sm text-muted-foreground">
          {t("alerts.routing.readOnly")}
        </p>
      )}
      {canManage && editing === null && (
        <div>
          <Button type="button" onClick={() => setEditing("new")}>
            <Plus aria-hidden="true" />
            {t("alerts.routing.new")}
          </Button>
        </div>
      )}
      {editing !== null && (
        <RoutingRuleForm
          key={editing === "new" ? "new" : editing.id}
          rule={editing === "new" ? null : editing}
          channels={channels.data?.channels ?? []}
          nextPosition={rules.data?.length ?? 0}
          onDone={() => setEditing(null)}
        />
      )}
      <FormError error={remove.error ?? reorder.error} />
      {rules.isPending ? (
        <LoadingState />
      ) : rules.isError ? (
        <ErrorState error={rules.error} onRetry={() => void rules.refetch()} />
      ) : rules.data.length === 0 ? (
        <EmptyState>{t("alerts.routing.empty")}</EmptyState>
      ) : (
        <div className="rounded-xl border bg-card">
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("alerts.routing.columns.order")}</TableHead>
                <TableHead>{t("alerts.routing.columns.name")}</TableHead>
                <TableHead>{t("alerts.routing.columns.match")}</TableHead>
                <TableHead>{t("alerts.routing.columns.channels")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("alerts.routing.edit")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {ordered.map((r, i) => (
                <TableRow key={r.id} data-testid="routing-rule-row">
                  <TableCell label={t("alerts.routing.columns.order")} className="whitespace-nowrap">
                    {r.is_default ? (
                      <Badge variant="secondary">{t("alerts.routing.defaultBadge")}</Badge>
                    ) : (
                      <span className="inline-flex items-center gap-1">
                        <span className="text-sm text-muted-foreground tabular-nums">{i + 1}</span>
                        {canManage && (
                          <>
                            <Button type="button" variant="ghost" size="icon" aria-label={t("alerts.routing.moveUp", { name: r.name })} disabled={i === 0 || reorder.isPending} onClick={() => move(i, -1)}>
                              <ArrowUp aria-hidden="true" />
                            </Button>
                            <Button type="button" variant="ghost" size="icon" aria-label={t("alerts.routing.moveDown", { name: r.name })} disabled={i >= movable - 1 || reorder.isPending} onClick={() => move(i, 1)}>
                              <ArrowDown aria-hidden="true" />
                            </Button>
                          </>
                        )}
                      </span>
                    )}
                  </TableCell>
                  <TableCell>
                    <span className="font-medium">{r.name}</span>
                    {!r.enabled && (
                      <Badge variant="muted" className="ml-2">
                        {t("alerts.routing.disabled")}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell label={t("alerts.routing.columns.match")} className="text-sm">
                    {summary(r).join(" · ")}
                  </TableCell>
                  <TableCell label={t("alerts.routing.columns.channels")} className="text-sm">
                    {r.channel_ids.map((id) => channelName.get(id) ?? id).join(", ")}
                  </TableCell>
                  <TableCell>
                    {canManage && (
                      <div className="flex flex-wrap justify-end gap-2 max-md:justify-start">
                        <Button type="button" variant="outline" size="sm" onClick={() => setEditing(r)}>
                          {t("alerts.routing.edit")}
                        </Button>
                        <ConfirmAction label={t("alerts.routing.delete")} confirmLabel={t("alerts.routing.confirmDelete")} destructive onConfirm={() => remove.mutate(r.id)} />
                      </div>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}
