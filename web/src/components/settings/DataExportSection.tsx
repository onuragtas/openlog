import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { accountPrivacyQuery, downloadExport, EXPORT_SIGNALS, orgExportsQuery, requestOrgExport, type DataExport, type ExportSignal } from "@/api/privacy";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { formatBytes, formatDateTime } from "@/lib/format";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { parseApiTime } from "./time";

/** Value of a datetime-local input for epoch milliseconds (local time). */
function toLocalInput(ms: number): string {
  const d = new Date(ms);
  return new Date(ms - d.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}

const STATUS_VARIANT = { pending: "warning", running: "warning", completed: "success", failed: "destructive", expired: "muted" } as const;

/** Exports with status, size, expiry and download (organization and personal exports). */
export function ExportList({ exports }: { exports: DataExport[] }) {
  const { t, i18n } = useTranslation();
  const download = useMutation({ mutationFn: (id: string) => downloadExport(id) });
  if (exports.length === 0) return <p className="text-sm text-muted-foreground">{t("privacy.empty")}</p>;
  return (
    <>
      <ul className="divide-y rounded-md border">
        {exports.map((e) => (
          <li key={e.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2 text-sm">
            <Badge variant={STATUS_VARIANT[e.status]}>{t(`privacy.status.${e.status}`)}</Badge>
            <span className="text-muted-foreground">
              {t("privacy.created")}: <DateTimeText value={e.created_at} relative />
            </span>
            {e.signals.length > 0 && <span>{e.signals.map((s) => t(`privacy.signal.${s}`)).join(", ")}</span>}
            {e.status === "completed" && <span>{formatBytes(e.size_bytes)}</span>}
            {e.truncated && <span className="text-xs text-muted-foreground">{t("privacy.truncated")}</span>}
            {e.status === "failed" && e.error && <span className="text-xs text-destructive">{e.error}</span>}
            {e.download_available && (
              <>
                <span className="text-xs text-muted-foreground">
                  {t("privacy.expires")}: <DateTimeText value={e.expires_at} />
                </span>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="sm:ml-auto"
                  disabled={download.isPending}
                  aria-label={t("privacy.downloadNamed", { date: formatDateTime(parseApiTime(e.created_at), i18n.resolvedLanguage ?? "en") })}
                  onClick={() => download.mutate(e.id)}
                >
                  <Download aria-hidden="true" />
                  {t("privacy.download")}
                </Button>
              </>
            )}
          </li>
        ))}
      </ul>
      <FormError error={download.error} />
    </>
  );
}

/** Settings → Organization → Data export (owners; D-107). */
export function DataExportSection() {
  const me = useMe().data;
  if (me?.role !== "owner" || me.auth !== "session") return null;
  return <DataExportForm />;
}

function DataExportForm() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const privacy = useQuery(accountPrivacyQuery());
  const enabled = privacy.data?.data_export_enabled ?? false;
  const list = useQuery({ ...orgExportsQuery(), enabled });
  const [signals, setSignals] = useState<ExportSignal[]>([]);
  const [from, setFrom] = useState(() => toLocalInput(Date.now() - 24 * 3_600_000));
  const [to, setTo] = useState(() => toLocalInput(Date.now()));
  const request = useMutation({
    mutationFn: () =>
      requestOrgExport(signals.length > 0 ? { signals, from: new Date(from).toISOString(), to: new Date(to).toISOString() } : { signals: [] }),
    onSuccess: (e) => {
      qc.setQueryData(orgExportsQuery().queryKey, (old) => [e, ...(old ?? [])]);
      void qc.invalidateQueries({ queryKey: orgExportsQuery().queryKey });
    },
  });
  if (privacy.isSuccess && !enabled) return null;

  return (
    <SettingsSection title={t("privacy.exportTitle")} description={t("privacy.exportDescription")}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          request.mutate();
        }}
      >
        <fieldset className="flex flex-wrap gap-x-4 gap-y-2">
          <legend className="mb-1 text-sm font-medium">{t("privacy.signals")}</legend>
          {EXPORT_SIGNALS.map((s) => (
            <label key={s} className="inline-flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={signals.includes(s)}
                onChange={(e) => setSignals((cur) => (e.target.checked ? [...cur, s] : cur.filter((x) => x !== s)))}
              />
              {t(`privacy.signal.${s}`)}
            </label>
          ))}
        </fieldset>
        {signals.length > 0 ? (
          <div className="flex flex-wrap gap-3">
            <label htmlFor={`${id}-from`} className="flex flex-col gap-1 text-sm">
              {t("privacy.from")}
              <Input id={`${id}-from`} type="datetime-local" value={from} max={to} onChange={(e) => setFrom(e.target.value)} className="w-auto" required />
            </label>
            <label htmlFor={`${id}-to`} className="flex flex-col gap-1 text-sm">
              {t("privacy.to")}
              <Input id={`${id}-to`} type="datetime-local" value={to} min={from} onChange={(e) => setTo(e.target.value)} className="w-auto" required />
            </label>
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">{t("privacy.noTelemetry")}</p>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <Button type="submit" size="sm" disabled={request.isPending}>
            {t("privacy.request")}
          </Button>
          {request.isSuccess && (
            <p role="status" className="text-xs text-muted-foreground">
              {t("privacy.requested")}
            </p>
          )}
        </div>
        <FormError error={request.error} />
      </form>
      {list.isPending ? <LoadingState /> : list.isError ? <ErrorState error={list.error} onRetry={() => void list.refetch()} /> : <ExportList exports={list.data} />}
    </SettingsSection>
  );
}
