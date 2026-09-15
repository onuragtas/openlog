import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { alertHolidayCalendarsQuery, createHolidayCalendar, deleteHolidayCalendar, updateHolidayCalendar, type AlertHolidayCalendar } from "@/api/alerts";
import { ConfirmAction } from "@/components/fleet/ConfirmAction";
import { FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { parseCalendarDates } from "@/lib/mute-schedule";
import { usePermissions } from "@/lib/org-writable";
import { Field, Section } from "./fields";

function CalendarForm({ calendar, onDone }: { calendar: AlertHolidayCalendar | null; onDone: () => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const [name, setName] = useState(calendar?.name ?? "");
  const [description, setDescription] = useState(calendar?.description ?? "");
  const [text, setText] = useState((calendar?.dates ?? []).join("\n"));
  const parsed = parseCalendarDates(text);
  const save = useMutation({
    mutationFn: () => {
      const input = { name: name.trim(), description, dates: parsed.dates };
      return calendar ? updateHolidayCalendar(calendar.id, input) : createHolidayCalendar(input);
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      onDone();
    },
  });
  const datesError = parsed.invalid.length ? t("alerts.calendars.invalidDates", { dates: parsed.invalid.join(", ") }) : undefined;
  return (
    <form
      className="flex flex-col gap-3 rounded-lg border p-3"
      data-testid="calendar-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (!datesError) save.mutate();
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field id={`${uid}-name`} label={t("alerts.calendars.name")}>
          <Input id={`${uid}-name`} required value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field id={`${uid}-desc`} label={t("alerts.calendars.description")}>
          <Input id={`${uid}-desc`} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
      </div>
      <Field id={`${uid}-dates`} label={t("alerts.calendars.dates")} hint={t("alerts.calendars.datesHint")} error={datesError}>
        <textarea
          id={`${uid}-dates`}
          rows={5}
          value={text}
          placeholder={"01-01\n04-23\n2026-03-20"}
          aria-invalid={!!datesError}
          onChange={(e) => setText(e.target.value)}
          className="rounded-md border border-input bg-background px-3 py-2 font-mono text-sm shadow-xs"
        />
      </Field>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={save.isPending || !!datesError}>
          {calendar ? t("alerts.calendars.save") : t("alerts.calendars.create")}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          {t("alerts.cancel")}
        </Button>
        <span className="text-xs text-muted-foreground">{t("alerts.calendars.count", { count: parsed.dates.length })}</span>
        <FormError error={save.error} />
      </div>
    </form>
  );
}

/** Holiday calendars used as exceptions by recurring mutes (alerting.md §5.2). */
export function HolidayCalendarsManager() {
  const { t } = useTranslation();
  const canManage = usePermissions().can("alerts.manage");
  const q = useQuery(alertHolidayCalendarsQuery());
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<AlertHolidayCalendar | "new" | null>(null);
  const remove = useMutation({ mutationFn: (id: string) => deleteHolidayCalendar(id), onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["alerts"] }) });
  return (
    <Section title={t("alerts.calendars.title")} description={t("alerts.calendars.description_")}>
      {canManage && editing === null && (
        <div>
          <Button type="button" variant="outline" onClick={() => setEditing("new")}>
            <Plus aria-hidden="true" />
            {t("alerts.calendars.new")}
          </Button>
        </div>
      )}
      {editing !== null && <CalendarForm key={editing === "new" ? "new" : editing.id} calendar={editing === "new" ? null : editing} onDone={() => setEditing(null)} />}
      <FormError error={remove.error} />
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.data.length === 0 ? (
        <EmptyState className="py-4">{t("alerts.calendars.empty")}</EmptyState>
      ) : (
        <ul className="divide-y rounded-lg border">
          {q.data.map((c) => (
            <li key={c.id} className="flex flex-col gap-2 p-3 sm:flex-row sm:items-start sm:justify-between" data-testid="calendar-row">
              <div className="min-w-0">
                <p className="font-medium">{c.name}</p>
                {c.description && <p className="text-xs text-muted-foreground">{c.description}</p>}
                <p className="mt-1 text-xs break-words text-muted-foreground">
                  {t("alerts.calendars.summary", { count: c.dates.length, mutes: c.mute_count })}: <span className="font-mono">{c.dates.slice(0, 8).join(", ")}{c.dates.length > 8 ? " …" : ""}</span>
                </p>
              </div>
              {canManage && (
                <div className="flex shrink-0 flex-wrap gap-2">
                  <Button type="button" variant="outline" size="sm" onClick={() => setEditing(c)}>
                    {t("alerts.calendars.edit")}
                  </Button>
                  <ConfirmAction label={t("alerts.calendars.delete")} confirmLabel={t("alerts.calendars.confirmDelete")} destructive onConfirm={() => remove.mutate(c.id)} />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
