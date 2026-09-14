import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Pencil, Plus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import type { Dashboard } from "@/api/dashboards";
import {
  createDashboardReport,
  dashboardReportsQuery,
  dashboardSettingsQuery,
  deleteDashboardReport,
  updateDashboardReport,
  updateDashboardSettings,
  type DashboardReport,
  type DashboardReportInput,
} from "@/api/dashboardSharing";
import { FormError } from "@/components/settings/common";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { lockedVariables, type VarValues } from "@/lib/dashboards";
import { formatDateTime } from "@/lib/format";

export interface ReportsDialogProps {
  dashboard: Dashboard;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  vars: VarValues | undefined;
}

const parseTs = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));
const REPORT_RANGES = ["1h", "6h", "24h", "7d", "30d"] as const;

/** Localized weekday names, 0 = Sunday. */
function weekdayNames(locale: string): string[] {
  const fmt = new Intl.DateTimeFormat(locale, { weekday: "long", timeZone: "UTC" });
  return Array.from({ length: 7 }, (_, i) => fmt.format(new Date(Date.UTC(2023, 0, 1 + i)))); // 2023-01-01 was a Sunday
}

/** Splits a recipients field (commas, semicolons, spaces, new lines). */
function parseRecipients(text: string): string[] {
  return [...new Set(text.split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean))];
}

const pad = (n: number) => String(n).padStart(2, "0");

function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function timeZones(): string[] {
  try {
    const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
    return intl.supportedValuesOf?.("timeZone") ?? [];
  } catch {
    return [];
  }
}

/** Scheduled e-mail reports of a dashboard (api.md "Scheduled reports"). */
export function ReportsDialog({ dashboard, open, onOpenChange, vars }: ReportsDialogProps) {
  const { t } = useTranslation();
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" title={t("dashboards.reports.title")} closeLabel={t("common.close")} className="w-full max-w-full sm:max-w-xl">
        {open && <ReportsBody dashboard={dashboard} vars={vars} />}
      </SheetContent>
    </Sheet>
  );
}

function ReportsBody({ dashboard, vars }: { dashboard: Dashboard; vars: VarValues | undefined }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const queryClient = useQueryClient();
  const reports = useQuery(dashboardReportsQuery(dashboard.id));
  const settings = useQuery(dashboardSettingsQuery());
  const [editing, setEditing] = useState<DashboardReport | "new" | null>(null);
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["dashboards", "reports", dashboard.id] });
  const remove = useMutation({ mutationFn: (id: string) => deleteDashboardReport(dashboard.id, id), onSuccess: invalidate });
  const days = weekdayNames(locale);

  if (reports.isPending) return <LoadingState />;
  if (reports.isError) return <ErrorState error={reports.error} onRetry={() => void reports.refetch()} />;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto p-4" data-testid="reports-dialog">
      <p className="text-sm text-muted-foreground">{t("dashboards.reports.hint")}</p>
      {editing ? (
        <ReportForm
          dashboard={dashboard}
          vars={vars}
          report={editing === "new" ? null : editing}
          onDone={() => {
            setEditing(null);
            invalidate();
          }}
          onCancel={() => setEditing(null)}
        />
      ) : (
        dashboard.can_edit && (
          <Button className="w-fit" onClick={() => setEditing("new")} data-testid="report-new">
            <Plus aria-hidden="true" />
            {t("dashboards.reports.create")}
          </Button>
        )
      )}

      {!editing && (
        <section className="flex flex-col gap-2">
          {reports.data.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("dashboards.reports.none")}</p>
          ) : (
            <ul className="flex flex-col gap-2" data-testid="report-list">
              {reports.data.map((r) => (
                <li key={r.id} className="flex min-w-0 flex-col gap-1 rounded-lg border px-3 py-2 text-sm" data-testid="report-item">
                  <div className="flex flex-wrap items-center gap-2">
                    <Mail className="size-4 text-muted-foreground" aria-hidden="true" />
                    <span className="min-w-0 truncate font-medium">{r.name || dashboard.name}</span>
                    {!r.enabled && <Badge variant="outline">{t("dashboards.reports.paused")}</Badge>}
                    {r.last_run && <Badge variant={r.last_run.status === "sent" ? "muted" : "outline"}>{t(`dashboards.reports.status.${r.last_run.status}`)}</Badge>}
                  </div>
                  <p className="text-xs break-words text-muted-foreground">
                    {r.frequency === "weekly"
                      ? t("dashboards.reports.scheduleWeekly", { day: days[r.weekday] ?? "", time: `${pad(r.hour)}:${pad(r.minute)}`, timezone: r.timezone })
                      : t("dashboards.reports.scheduleDaily", { time: `${pad(r.hour)}:${pad(r.minute)}`, timezone: r.timezone })}
                    {" · "}
                    {t("dashboards.reports.rangeOf", { range: r.range })}
                    {r.next_run_at && ` · ${t("dashboards.reports.next", { time: formatDateTime(parseTs(r.next_run_at), locale) })}`}
                  </p>
                  <p className="text-xs break-all text-muted-foreground">{r.recipients.join(", ")}</p>
                  {r.last_run?.error && <p className="text-xs break-words text-destructive-text">{r.last_run.error}</p>}
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" variant="outline" onClick={() => setEditing(r)} aria-label={t("dashboards.reports.editNamed", { name: r.name || dashboard.name })}>
                      <Pencil aria-hidden="true" />
                      {t("dashboards.reports.edit")}
                    </Button>
                    <DeleteButton pending={remove.isPending && remove.variables === r.id} onDelete={() => remove.mutate(r.id)} />
                  </div>
                </li>
              ))}
            </ul>
          )}
          <FormError error={remove.error} />
        </section>
      )}

      {!editing && settings.data?.can_edit && <ReportDomains domains={settings.data.report_domains} sharesEnabled={settings.data.share_links_enabled} />}
    </div>
  );
}

function DeleteButton({ pending, onDelete }: { pending: boolean; onDelete: () => void }) {
  const { t } = useTranslation();
  const [confirm, setConfirm] = useState(false);
  return confirm ? (
    <Button size="sm" variant="destructive" onClick={onDelete} disabled={pending}>
      {t("dashboards.reports.confirmDelete")}
    </Button>
  ) : (
    <Button size="sm" variant="outline" onClick={() => setConfirm(true)}>
      {t("dashboards.reports.delete")}
    </Button>
  );
}

function ReportForm({ dashboard, vars, report, onDone, onCancel }: { dashboard: Dashboard; vars: VarValues | undefined; report: DashboardReport | null; onDone: () => void; onCancel: () => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const uid = useId();
  const me = useMe().data;
  const [name, setName] = useState(report?.name ?? "");
  const [frequency, setFrequency] = useState<"daily" | "weekly">(report?.frequency ?? "daily");
  const [weekday, setWeekday] = useState(report?.weekday ?? 1);
  const [time, setTime] = useState(report ? `${pad(report.hour)}:${pad(report.minute)}` : "09:00");
  const [timezone, setTimezone] = useState(report?.timezone ?? browserTimeZone());
  const [recipients, setRecipients] = useState((report?.recipients ?? (me?.user?.email ? [me.user.email] : [])).join(", "));
  const [language, setLanguage] = useState<"en" | "tr">(report?.language ?? (locale === "tr" ? "tr" : "en"));
  const [range, setRange] = useState(report?.range ?? "24h");
  const [enabled, setEnabled] = useState(report?.enabled ?? true);
  const [lock, setLock] = useState(true);
  const zones = timeZones();

  const save = useMutation({
    mutationFn: () => {
      const [h, m] = time.split(":").map(Number);
      const body: DashboardReportInput = {
        name: name.trim(),
        frequency,
        weekday,
        hour: h ?? 9,
        minute: m ?? 0,
        timezone: timezone.trim(),
        recipients: parseRecipients(recipients),
        language,
        range,
        enabled,
        variables: report && !lock ? report.variables : lock ? lockedVariables(dashboard.variables, vars) : {},
      };
      return report ? updateDashboardReport(dashboard.id, report.id, body) : createDashboardReport(dashboard.id, body);
    },
    onSuccess: onDone,
  });
  const days = weekdayNames(locale);

  return (
    <form
      className="flex flex-col gap-3 rounded-lg border p-3"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      data-testid="report-form"
    >
      <h3 className="text-sm font-semibold">{report ? t("dashboards.reports.editTitle") : t("dashboards.reports.create")}</h3>
      <div className="flex flex-col gap-1">
        <Label htmlFor={`${uid}-name`}>{t("dashboards.reports.name")}</Label>
        <Input id={`${uid}-name`} value={name} maxLength={100} placeholder={dashboard.name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${uid}-freq`}>{t("dashboards.reports.frequency")}</Label>
          <NativeSelect
            id={`${uid}-freq`}
            value={frequency}
            onChange={(e) => {
              const f = e.target.value as "daily" | "weekly";
              setFrequency(f);
              if (!report) setRange(f === "weekly" ? "7d" : "24h");
            }}
          >
            <option value="daily">{t("dashboards.reports.daily")}</option>
            <option value="weekly">{t("dashboards.reports.weekly")}</option>
          </NativeSelect>
        </div>
        {frequency === "weekly" && (
          <div className="flex flex-col gap-1">
            <Label htmlFor={`${uid}-day`}>{t("dashboards.reports.weekday")}</Label>
            <NativeSelect id={`${uid}-day`} value={String(weekday)} onChange={(e) => setWeekday(Number(e.target.value))}>
              {days.map((d, i) => (
                <option key={i} value={i}>
                  {d}
                </option>
              ))}
            </NativeSelect>
          </div>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${uid}-time`}>{t("dashboards.reports.time")}</Label>
          <Input id={`${uid}-time`} type="time" required value={time} onChange={(e) => setTime(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${uid}-tz`}>{t("dashboards.reports.timezone")}</Label>
          <Input id={`${uid}-tz`} list={`${uid}-tzs`} value={timezone} onChange={(e) => setTimezone(e.target.value)} />
          {zones.length > 0 && (
            <datalist id={`${uid}-tzs`}>
              {zones.map((z) => (
                <option key={z} value={z} />
              ))}
            </datalist>
          )}
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${uid}-range`}>{t("dashboards.reports.range")}</Label>
          <NativeSelect id={`${uid}-range`} value={range} onChange={(e) => setRange(e.target.value)}>
            {[...new Set([...REPORT_RANGES, range])].map((r) => (
              <option key={r} value={r}>
                {t("dashboards.reports.rangeOf", { range: r })}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${uid}-lang`}>{t("dashboards.reports.language")}</Label>
          <NativeSelect id={`${uid}-lang`} value={language} onChange={(e) => setLanguage(e.target.value as "en" | "tr")}>
            <option value="en">English</option>
            <option value="tr">Türkçe</option>
          </NativeSelect>
        </div>
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor={`${uid}-rcpt`}>{t("dashboards.reports.recipients")}</Label>
        <textarea
          id={`${uid}-rcpt`}
          value={recipients}
          rows={2}
          onChange={(e) => setRecipients(e.target.value)}
          className="min-h-16 w-full min-w-0 rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
        />
        <p className="text-xs text-muted-foreground">{t(dashboard.visibility === "private" ? "dashboards.reports.recipientsPrivate" : "dashboards.reports.recipientsHint")}</p>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="size-4" />
        {t("dashboards.reports.enabled")}
      </label>
      {dashboard.variables.length > 0 && (
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={lock} onChange={(e) => setLock(e.target.checked)} className="size-4" />
          {t("dashboards.reports.lockVariables")}
        </label>
      )}
      <FormError error={save.error} />
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={save.isPending} data-testid="report-save">
          {t("dashboards.reports.save")}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}

function ReportDomains({ domains, sharesEnabled }: { domains: string[]; sharesEnabled: boolean }) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const [text, setText] = useState(domains.join(", "));
  const save = useMutation({
    mutationFn: () => updateDashboardSettings({ share_links_enabled: sharesEnabled, report_domains: parseRecipients(text) }),
    onSuccess: (st) => {
      queryClient.setQueryData(dashboardSettingsQuery().queryKey, st);
      setText(st.report_domains.join(", "));
    },
  });
  return (
    <form
      className="flex flex-col gap-2 border-t pt-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <Label htmlFor={`${uid}-domains`}>{t("dashboards.reports.domains")}</Label>
      <Input id={`${uid}-domains`} value={text} placeholder="example.com, partner.io" onChange={(e) => setText(e.target.value)} />
      <p className="text-xs text-muted-foreground">{t("dashboards.reports.domainsHint")}</p>
      <FormError error={save.error} />
      <Button type="submit" variant="outline" className="w-fit" disabled={save.isPending}>
        {t("dashboards.reports.saveDomains")}
      </Button>
    </form>
  );
}
