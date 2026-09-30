import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  deleteDiskSpaceSettings,
  DISK_ALERT_LEVELS,
  DISK_SHED_LEVELS,
  DISK_SHED_ORDER,
  diskSpaceQuery,
  putDiskSpaceSettings,
  type DiskAlertLevel,
  type DiskShedLevel,
  type DiskSpace,
  type DiskSpaceSettingsInput,
  type DiskStatus,
} from "@/api/diskspace";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { formatBytes } from "@/lib/format";
import { FormError, SettingsSection } from "./common";

type Draft = {
  alert: Record<DiskAlertLevel, string>;
  shedEnabled: boolean;
  shed: Record<DiskShedLevel, string>;
};

function draftOf(d: DiskSpace): Draft {
  const c = d.configured;
  const v = (k: DiskAlertLevel | DiskShedLevel) => {
    const x = c?.[k];
    return x === null || x === undefined ? "" : String(x);
  };
  return {
    alert: { warn_percent: v("warn_percent"), high_percent: v("high_percent"), hysteresis: v("hysteresis") },
    shedEnabled: d.effective.shed_enabled,
    shed: {
      shed_start_percent: v("shed_start_percent"),
      shed_stop_percent: v("shed_stop_percent"),
      shed_min_partitions: v("shed_min_partitions"),
      shed_max_drops_per_run: v("shed_max_drops_per_run"),
    },
  };
}

/** "ok" until a disk has reached a level; then which one, so the colour matches the server's own wording. */
function levelVariant(level: number, high: number): "ok" | "warning" | "critical" {
  if (level <= 0) return "ok";
  return level >= high ? "critical" : "warning";
}

function DiskRow({ disk, high }: { disk: DiskStatus; high: number }) {
  const { t } = useTranslation();
  const variant = levelVariant(disk.level, high);
  return (
    <tr className="border-t">
      <td className="py-2 pr-3 font-medium break-all">{disk.host}</td>
      <td className="py-2 pr-3 break-all">{disk.disk}</td>
      <td className="py-2 pr-3 text-right tabular-nums">{Math.round(disk.used_percent)}%</td>
      <td className="py-2 pr-3 text-right tabular-nums whitespace-nowrap">{formatBytes(disk.free_bytes)}</td>
      <td className="py-2 pr-3 text-right tabular-nums whitespace-nowrap">{formatBytes(disk.total_bytes)}</td>
      <td className="py-2">
        <Badge variant={variant === "ok" ? "outline" : "destructive"}>
          {variant === "ok" ? t("storage.disk.levels.ok") : t(`storage.disk.levels.${variant}`, { level: disk.level })}
        </Badge>
        {disk.broken && (
          <Badge variant="destructive" className="ml-2">
            {t("storage.disk.broken")}
          </Badge>
        )}
      </td>
    </tr>
  );
}

/** Settings → Storage: how full the ClickHouse disks are, the levels they are reported at, and what is deleted. */
export function DiskSpaceSettings() {
  const q = useQuery(diskSpaceQuery());
  if (q.isLoading) return <LoadingState />;
  if (q.error) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return null;
  // Not keyed on the data: a save must not remount the form (it would drop the "saved" message).
  return <DiskSpaceForm data={q.data} />;
}

function DiskSpaceForm({ data }: { data: DiskSpace }) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const [draft, setDraft] = useState<Draft>(() => draftOf(data));
  const [invalid, setInvalid] = useState<string | null>(null);
  const onDone = (next: DiskSpace) => {
    qc.setQueryData(diskSpaceQuery().queryKey, next);
    setDraft(draftOf(next));
  };
  const save = useMutation({ mutationFn: putDiskSpaceSettings, onSuccess: onDone });
  const reset = useMutation({ mutationFn: deleteDiskSpaceSettings, onSuccess: onDone });
  const busy = save.isPending || reset.isPending;

  // Whole numbers only, and "" means "use the built-in value".
  const num = (raw: string): number | null | undefined => {
    const s = raw.trim();
    if (s === "") return null;
    const n = Number(s);
    return Number.isSafeInteger(n) && n >= 0 ? n : undefined;
  };

  function submit() {
    const body: DiskSpaceSettingsInput = {};
    for (const k of DISK_ALERT_LEVELS) {
      const v = num(draft.alert[k]);
      if (v === undefined) return setInvalid(t("storage.disk.invalid"));
      body[k] = v;
    }
    for (const k of DISK_SHED_LEVELS) {
      const v = num(draft.shed[k]);
      if (v === undefined) return setInvalid(t("storage.disk.invalid"));
      body[k] = v;
    }
    // Unchecked sends null rather than false, so turning it off leaves the row as clean as never having set it.
    body.shed_enabled = draft.shedEnabled ? true : null;

    const warn = body.warn_percent ?? data.defaults.warn_percent;
    const high = body.high_percent ?? data.defaults.high_percent;
    const start = body.shed_start_percent ?? data.defaults.shed_start_percent;
    const stop = body.shed_stop_percent ?? data.defaults.shed_stop_percent;
    // The same three rules the server enforces, said here so they read as sentences instead of coming back as a
    // rejected request.
    if (warn >= high) return setInvalid(t("storage.disk.order"));
    if (stop >= start) return setInvalid(t("storage.disk.shed.stopOrder"));
    if (start < high) return setInvalid(t("storage.disk.shed.belowHigh", { high }));
    setInvalid(null);
    save.mutate(body);
  }

  return (
    <div className="flex flex-col gap-6">
      <SettingsSection title={t("storage.disk.title")} description={t("storage.disk.description")}>
        {data.checked_at === "" ? (
          <p className="text-sm text-muted-foreground">{t("storage.disk.notMeasuredYet")}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="pb-2 pr-3 font-medium">{t("storage.disk.columns.host")}</th>
                  <th className="pb-2 pr-3 font-medium">{t("storage.disk.columns.disk")}</th>
                  <th className="pb-2 pr-3 text-right font-medium">{t("storage.disk.columns.used")}</th>
                  <th className="pb-2 pr-3 text-right font-medium">{t("storage.disk.columns.free")}</th>
                  <th className="pb-2 pr-3 text-right font-medium">{t("storage.disk.columns.total")}</th>
                  <th className="pb-2 font-medium">{t("storage.disk.columns.level")}</th>
                </tr>
              </thead>
              <tbody>
                {data.disks.map((d) => (
                  <DiskRow key={`${d.host}/${d.disk}`} disk={d} high={data.effective.high_percent} />
                ))}
              </tbody>
            </table>
          </div>
        )}
        <p className="text-xs text-muted-foreground">{t("storage.disk.remoteNote")}</p>
      </SettingsSection>

      <form
        className="flex flex-col gap-6"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <SettingsSection title={t("storage.disk.alerts.title")} description={t("storage.disk.alerts.description")}>
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            {DISK_ALERT_LEVELS.map((k) => (
              <div key={k} className="flex min-w-0 flex-col gap-1.5 rounded-lg border p-3" data-testid={`disk-level-${k}`}>
                <Label htmlFor={`${id}-${k}`}>{t(`storage.disk.settings.${k}`)}</Label>
                <div className="flex flex-wrap items-baseline gap-2">
                  <span className="text-lg font-semibold tabular-nums">{data.effective[k]}</span>
                  <Badge variant="outline">
                    {t(data.configured?.[k] == null ? "storage.disk.sources.default" : "storage.disk.sources.configured")}
                  </Badge>
                </div>
                <p className="text-xs text-muted-foreground">{t("storage.disk.builtIn", { value: data.defaults[k] })}</p>
                {data.can_manage && (
                  <Input
                    id={`${id}-${k}`}
                    inputMode="numeric"
                    placeholder={t("storage.disk.useDefault")}
                    value={draft.alert[k]}
                    onChange={(e) => setDraft({ ...draft, alert: { ...draft.alert, [k]: e.target.value } })}
                  />
                )}
              </div>
            ))}
          </div>
        </SettingsSection>

        <SettingsSection title={t("storage.disk.shed.title")} description={t("storage.disk.shed.description")}>
          <p role="note" className="rounded-md border border-destructive/60 bg-destructive/10 px-3 py-2 text-sm">
            {t("storage.disk.shed.warning")}
          </p>
          <label className="inline-flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-0.5 size-4 accent-primary"
              checked={draft.shedEnabled}
              disabled={!data.can_manage}
              onChange={(e) => setDraft({ ...draft, shedEnabled: e.target.checked })}
            />
            <span>
              {t("storage.disk.shed.enable")}
              {!draft.shedEnabled && <span className="ml-2 text-muted-foreground">{t("storage.disk.shed.currentlyOff")}</span>}
            </span>
          </label>

          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
            {DISK_SHED_LEVELS.map((k) => (
              <div key={k} className="flex min-w-0 flex-col gap-1.5 rounded-lg border p-3" data-testid={`disk-shed-${k}`}>
                <Label htmlFor={`${id}-${k}`}>{t(`storage.disk.shed.settings.${k}`)}</Label>
                <div className="flex flex-wrap items-baseline gap-2">
                  <span className="text-lg font-semibold tabular-nums">{data.effective[k]}</span>
                  <Badge variant="outline">
                    {t(data.configured?.[k] == null ? "storage.disk.sources.default" : "storage.disk.sources.configured")}
                  </Badge>
                </div>
                <p className="text-xs text-muted-foreground">{t("storage.disk.builtIn", { value: data.defaults[k] })}</p>
                {data.can_manage && (
                  <Input
                    id={`${id}-${k}`}
                    inputMode="numeric"
                    placeholder={t("storage.disk.useDefault")}
                    value={draft.shed[k]}
                    onChange={(e) => setDraft({ ...draft, shed: { ...draft.shed, [k]: e.target.value } })}
                  />
                )}
              </div>
            ))}
          </div>

          <div className="text-xs text-muted-foreground">
            <p>{t("storage.disk.shed.orderTitle")}</p>
            <ol className="mt-1 list-decimal pl-5">
              {DISK_SHED_ORDER.map((u) => (
                <li key={u}>{t(`storage.disk.shed.order.${u}`)}</li>
              ))}
            </ol>
            <p className="mt-2">{t("storage.disk.shed.never")}</p>
          </div>
        </SettingsSection>

        {invalid && (
          <p role="alert" className="text-sm text-destructive">
            {invalid}
          </p>
        )}
        {data.can_manage ? (
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" disabled={busy}>
              {t("storage.disk.save")}
            </Button>
            {data.configured && (
              <Button type="button" variant="outline" disabled={busy} onClick={() => reset.mutate()}>
                {t("storage.disk.reset")}
              </Button>
            )}
            {(save.isSuccess || reset.isSuccess) && (
              <span className="text-sm text-muted-foreground">{t("storage.disk.saved", { seconds: data.interval_seconds })}</span>
            )}
            <FormError error={save.error ?? reset.error} />
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">{t("storage.disk.readOnly")}</p>
        )}
        <p className="text-xs text-muted-foreground">{t("storage.disk.hint")}</p>
      </form>
    </div>
  );
}
