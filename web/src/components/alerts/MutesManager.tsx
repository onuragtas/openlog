import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2, X } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import {
  alertHolidayCalendarsQuery,
  alertMutesQuery,
  alertRulesQuery,
  createAlertMute,
  deleteAlertMute,
  muteSchedulePreviewQuery,
  updateAlertMute,
  type AlertMute,
  type AlertMuteInput,
  type AlertMuteMatcher,
  type AlertMuteScheduleInput,
  type AlertRule,
} from "@/api/alerts";
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
import {
  DEFAULT_MONTHLY,
  monthlyRRule,
  monthlySummary,
  normalizeDates,
  ORDINALS,
  parseMonthlyRRule,
  WEEK_DAYS,
  type MonthlyDay,
  type MonthlyForm,
  type Ordinal,
  type Recurrence,
  type WeekDay,
} from "@/lib/mute-schedule";
import { usePermissions } from "@/lib/org-writable";
import { fromDateTimeLocal, toDateTimeLocal } from "@/lib/time";
import { Field } from "./fields";
import { HolidayCalendarsManager } from "./HolidayCalendarsManager";

const parse = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));

function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

/** Human summary of a stored schedule: weekly days or the monthly rule, local times and zone. */
function ScheduleSummary({ schedule }: { schedule: NonNullable<AlertMute["schedule"]> }) {
  const { t } = useTranslation();
  const monthly = monthlySummary(schedule.rrule);
  let when: string;
  if (schedule.rrule?.startsWith("FREQ=MONTHLY")) {
    when = !monthly
      ? schedule.rrule
      : monthly.key === "monthday"
        ? t("alerts.mutes.monthly.summaryMonthday", { days: monthly.days })
        : monthly.key === "nthWeekday"
          ? t("alerts.mutes.monthly.summaryNthWeekday", { ordinal: t(`alerts.mutes.monthly.ordinals.${monthly.ordinal}`) })
          : t("alerts.mutes.monthly.summaryNthDay", { ordinal: t(`alerts.mutes.monthly.ordinals.${monthly.ordinal}`), day: t(`alerts.mutes.dayNames.${monthly.day}`) });
  } else {
    when = schedule.days.map((d) => t(`alerts.mutes.dayNames.${d}`)).join(", ");
  }
  const exceptions = schedule.exdates.length + schedule.holiday_calendar_ids.length;
  return (
    <p data-testid="mute-schedule">
      {when} {schedule.start_time}–{schedule.end_time} ({schedule.timezone})
      {exceptions > 0 && (
        <span className="block text-xs text-muted-foreground">
          {t("alerts.mutes.exceptionsSummary", { dates: schedule.exdates.length, calendars: schedule.holiday_calendar_ids.length })}
        </span>
      )}
    </p>
  );
}

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
  // Recurring schedule (alerting.md §5.2)
  const sched = mute?.schedule;
  const storedMonthly = sched?.rrule?.startsWith("FREQ=MONTHLY") ?? false;
  const [recurring, setRecurring] = useState(!!sched);
  const [recurrence, setRecurrence] = useState<Recurrence>(storedMonthly ? "monthly" : "weekly");
  const [monthly, setMonthly] = useState<MonthlyForm>(() => parseMonthlyRRule(sched?.rrule) ?? DEFAULT_MONTHLY);
  // A stored monthly rule the form cannot represent is kept as is until the user picks another shape.
  const [customRRule, setCustomRRule] = useState<string | null>(storedMonthly && !parseMonthlyRRule(sched?.rrule) ? sched!.rrule! : null);
  const [timezone, setTimezone] = useState(sched?.timezone ?? browserTimeZone());
  const [days, setDays] = useState<WeekDay[]>((sched?.days as WeekDay[] | undefined)?.length ? (sched!.days as WeekDay[]) : ["mon", "tue", "wed", "thu", "fri"]);
  const [startTime, setStartTime] = useState(sched?.start_time ?? "22:00");
  const [endTime, setEndTime] = useState(sched?.end_time ?? "06:00");
  const [exdates, setExdates] = useState<string[]>(sched?.exdates ?? []);
  const [newDate, setNewDate] = useState("");
  const [calendarIds, setCalendarIds] = useState<string[]>(sched?.holiday_calendar_ids ?? []);
  const calendars = useQuery({ ...alertHolidayCalendarsQuery(), enabled: recurring });
  const id = (n: string) => `${uid}-${n}`;

  const rrule = recurrence === "monthly" ? (customRRule ?? monthlyRRule(monthly)) : null;
  const scheduleInput: AlertMuteScheduleInput | null = !recurring
    ? null
    : {
        timezone: timezone.trim(),
        ...(recurrence === "weekly" ? { days: WEEK_DAYS.filter((d) => days.includes(d)) } : { rrule: rrule ?? "" }),
        start_time: startTime,
        end_time: endTime,
        from: sched?.from ?? null,
        until: sched?.until ?? null,
        exdates,
        holiday_calendar_ids: calendarIds,
      };
  const schedulePreviewable = recurring && (recurrence === "weekly" ? days.length > 0 : !!rrule) && /^\d{2}:\d{2}$/.test(startTime) && /^\d{2}:\d{2}$/.test(endTime);
  const debounced = useDebounced(schedulePreviewable ? JSON.stringify(scheduleInput) : "", 500);
  const upcoming = useQuery(muteSchedulePreviewQuery(debounced ? (JSON.parse(debounced) as AlertMuteScheduleInput) : null));

  const save = useMutation({
    mutationFn: () => {
      const s = fromDateTimeLocal(starts);
      const e = fromDateTimeLocal(ends);
      const input: AlertMuteInput = {
        name: name.trim(),
        comment,
        rule_ids: ruleIds,
        matchers: matchers.filter((m) => m.label.trim()),
        ...(scheduleInput
          ? { schedule: scheduleInput }
          : {
              starts_at: s === null ? starts : new Date(s).toISOString(),
              ends_at: e === null ? ends : new Date(e).toISOString(),
              schedule: null,
            }),
      };
      return mute ? updateAlertMute(mute.id, input) : createAlertMute(input);
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      onDone();
    },
  });
  const setMonthlyField = (patch: Partial<MonthlyForm>) => {
    setCustomRRule(null);
    setMonthly((m) => ({ ...m, ...patch }));
  };
  const addDate = () => {
    const { dates } = normalizeDates([...exdates, newDate]);
    setExdates(dates);
    setNewDate("");
  };
  const monthDaysInvalid = recurrence === "monthly" && monthly.mode === "monthday" && !customRRule && !rrule;

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
        {!recurring && (
          <>
            <Field id={id("starts")} label={t("alerts.mutes.starts")}>
              <Input id={id("starts")} type="datetime-local" value={starts} onChange={(e) => setStarts(e.target.value)} />
            </Field>
            <Field id={id("ends")} label={t("alerts.mutes.ends")}>
              <Input id={id("ends")} type="datetime-local" value={ends} onChange={(e) => setEnds(e.target.value)} />
            </Field>
          </>
        )}
      </div>
      <fieldset className="flex min-w-0 flex-col gap-3 rounded-lg border p-3">
        <legend className="px-1 text-sm font-medium">{t("alerts.mutes.recurring")}</legend>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={recurring} onChange={(e) => setRecurring(e.target.checked)} />
          {t("alerts.mutes.recurringToggle")}
        </label>
        {recurring && (
          <>
            <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
              <Field id={id("freq")} label={t("alerts.mutes.frequency")}>
                <NativeSelect id={id("freq")} value={recurrence} onChange={(e) => setRecurrence(e.target.value as Recurrence)}>
                  <option value="weekly">{t("alerts.mutes.frequencies.weekly")}</option>
                  <option value="monthly">{t("alerts.mutes.frequencies.monthly")}</option>
                </NativeSelect>
              </Field>
              <Field id={id("tz")} label={t("alerts.mutes.timezone")}>
                <Input id={id("tz")} value={timezone} placeholder="Europe/Istanbul" onChange={(e) => setTimezone(e.target.value)} />
              </Field>
              <Field id={id("stime")} label={t("alerts.mutes.startTime")}>
                <Input id={id("stime")} type="time" value={startTime} onChange={(e) => setStartTime(e.target.value)} />
              </Field>
              <Field id={id("etime")} label={t("alerts.mutes.endTime")} hint={t("alerts.mutes.endTimeHint")}>
                <Input id={id("etime")} type="time" value={endTime} onChange={(e) => setEndTime(e.target.value)} />
              </Field>
            </div>
            {recurrence === "weekly" ? (
              <div role="group" aria-label={t("alerts.mutes.days")} className="flex flex-wrap gap-2">
                {WEEK_DAYS.map((d) => (
                  <label key={d} className="flex min-h-10 items-center gap-2 rounded-md border px-2 py-1 text-sm has-checked:border-primary">
                    <input type="checkbox" checked={days.includes(d)} onChange={(e) => setDays(e.target.checked ? [...days, d] : days.filter((x) => x !== d))} />
                    {t(`alerts.mutes.dayNames.${d}`)}
                  </label>
                ))}
              </div>
            ) : (
              <div className="flex flex-col gap-3" data-testid="mute-monthly">
                <div role="radiogroup" aria-label={t("alerts.mutes.monthly.mode")} className="flex flex-wrap gap-2">
                  {(["monthday", "weekday"] as const).map((m) => (
                    <label key={m} className="flex min-h-10 items-center gap-2 rounded-md border px-2 py-1 text-sm has-checked:border-primary">
                      <input type="radio" name={id("mmode")} checked={monthly.mode === m && !customRRule} onChange={() => setMonthlyField({ mode: m })} />
                      {t(`alerts.mutes.monthly.modes.${m}`)}
                    </label>
                  ))}
                </div>
                {customRRule ? (
                  <p className="text-sm">
                    {t("alerts.mutes.monthly.custom")}: <code className="font-mono">{customRRule}</code>
                  </p>
                ) : monthly.mode === "monthday" ? (
                  <Field id={id("mdays")} label={t("alerts.mutes.monthly.monthDays")} hint={t("alerts.mutes.monthly.monthDaysHint")} error={monthDaysInvalid ? t("alerts.mutes.monthly.monthDaysInvalid") : undefined}>
                    <Input id={id("mdays")} value={monthly.monthDays} placeholder="1, 15, -1" aria-invalid={monthDaysInvalid} onChange={(e) => setMonthlyField({ monthDays: e.target.value })} />
                  </Field>
                ) : (
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field id={id("ord")} label={t("alerts.mutes.monthly.ordinal")}>
                      <NativeSelect id={id("ord")} value={monthly.ordinal} onChange={(e) => setMonthlyField({ ordinal: Number(e.target.value) as Ordinal })}>
                        {ORDINALS.map((o) => (
                          <option key={o} value={o}>
                            {t(`alerts.mutes.monthly.ordinals.${o}`)}
                          </option>
                        ))}
                      </NativeSelect>
                    </Field>
                    <Field id={id("mday")} label={t("alerts.mutes.monthly.day")}>
                      <NativeSelect id={id("mday")} value={monthly.day} onChange={(e) => setMonthlyField({ day: e.target.value as MonthlyDay })}>
                        {WEEK_DAYS.map((d) => (
                          <option key={d} value={d}>
                            {t(`alerts.mutes.dayNamesLong.${d}`)}
                          </option>
                        ))}
                        <option value="weekday">{t("alerts.mutes.monthly.weekday")}</option>
                      </NativeSelect>
                    </Field>
                  </div>
                )}
              </div>
            )}
            <fieldset className="flex min-w-0 flex-col gap-2">
              <legend className="mb-1 text-sm font-medium">{t("alerts.mutes.exceptions")}</legend>
              <p className="text-xs text-muted-foreground">{t("alerts.mutes.exceptionsHint")}</p>
              <div className="flex flex-wrap items-end gap-2">
                <Field id={id("exdate")} label={t("alerts.mutes.exceptionDate")}>
                  <Input id={id("exdate")} type="date" value={newDate} onChange={(e) => setNewDate(e.target.value)} />
                </Field>
                <Button type="button" variant="outline" className="min-h-10" disabled={!newDate} onClick={addDate}>
                  <Plus aria-hidden="true" />
                  {t("alerts.mutes.addException")}
                </Button>
              </div>
              {exdates.length > 0 && (
                <ul className="flex flex-wrap gap-2" aria-label={t("alerts.mutes.exceptionDates")}>
                  {exdates.map((d) => (
                    <li key={d}>
                      <Badge variant="secondary" className="gap-1 font-mono">
                        {d}
                        <button type="button" className="rounded-sm p-1 hover:bg-muted" aria-label={t("alerts.mutes.removeException", { date: d })} onClick={() => setExdates(exdates.filter((x) => x !== d))}>
                          <X className="size-3" aria-hidden="true" />
                        </button>
                      </Badge>
                    </li>
                  ))}
                </ul>
              )}
              {(calendars.data ?? []).length > 0 && (
                <div role="group" aria-label={t("alerts.mutes.holidayCalendars")} className="flex flex-wrap gap-2">
                  {calendars.data!.map((c) => (
                    <label key={c.id} className="flex min-h-10 items-center gap-2 rounded-md border px-2 py-1 text-sm has-checked:border-primary">
                      <input type="checkbox" checked={calendarIds.includes(c.id)} onChange={(e) => setCalendarIds(e.target.checked ? [...calendarIds, c.id] : calendarIds.filter((x) => x !== c.id))} />
                      {c.name} <span className="text-xs text-muted-foreground">({c.dates.length})</span>
                    </label>
                  ))}
                </div>
              )}
            </fieldset>
            <div className="text-sm" aria-live="polite" data-testid="mute-upcoming">
              <p className="font-medium">{t("alerts.mutes.upcoming")}</p>
              {!debounced ? (
                <p className="text-muted-foreground">{t("alerts.mutes.upcomingInvalid")}</p>
              ) : upcoming.isError ? (
                <FormError error={upcoming.error} />
              ) : !upcoming.data ? (
                <p className="text-muted-foreground">{t("common.loading")}</p>
              ) : upcoming.data.length === 0 ? (
                <p className="text-muted-foreground">{t("alerts.mutes.upcomingNone")}</p>
              ) : (
                <ol className="list-inside list-decimal text-muted-foreground">
                  {upcoming.data.map((o) => (
                    <li key={o.starts_at}>
                      <DateTimeText value={o.starts_at} /> – <DateTimeText value={o.ends_at} />
                    </li>
                  ))}
                </ol>
              )}
            </div>
          </>
        )}
      </fieldset>
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
        <Button type="submit" disabled={save.isPending || monthDaysInvalid}>
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
  const canWrite = usePermissions().can("alerts.write");
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
                // The server's `active` wins: `now` refreshes every 30 s, so a mute created in a later minute than the last
                // refresh would otherwise show as scheduled until the next tick.
                const status = m.active ? "active" : m.schedule ? (now < e ? "scheduled" : "ended") : now < s ? "scheduled" : now < e ? "active" : "ended";
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
                    <TableCell label={t("alerts.mutes.columns.window")} className="text-sm">
                      {m.schedule && <ScheduleSummary schedule={m.schedule} />}
                      <span className={m.schedule ? "text-xs whitespace-nowrap text-muted-foreground" : "whitespace-nowrap"}>
                        {m.schedule && `${t("alerts.mutes.occurrence")}: `}
                        <DateTimeText value={m.starts_at} /> – <DateTimeText value={m.ends_at} />
                      </span>
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
      <HolidayCalendarsManager />
    </div>
  );
}
