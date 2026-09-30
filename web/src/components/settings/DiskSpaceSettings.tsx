import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  deleteDiskSpaceSettings,
  DISK_SPACE_LEVELS,
  diskSpaceQuery,
  putDiskSpaceSettings,
  type DiskSpace,
  type DiskSpaceLevel,
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

type Draft = Record<DiskSpaceLevel, string>;

function draftOf(d: DiskSpace): Draft {
  const c = d.configured;
  const v = (k: DiskSpaceLevel) => {
    const x = c?.[k];
    return x === null || x === undefined ? "" : String(x);
  };
  return { warn_percent: v("warn_percent"), high_percent: v("high_percent"), hysteresis: v("hysteresis") };
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

/** Settings → Storage: how full the ClickHouse disks are, and the levels they are reported at. */
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

  return (
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

      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          const body: DiskSpaceSettingsInput = {};
          for (const k of DISK_SPACE_LEVELS) {
            const raw = draft[k].trim();
            if (raw === "") {
              body[k] = null;
              continue;
            }
            const n = Number(raw);
            if (!Number.isSafeInteger(n) || n < 0) {
              setInvalid(t("storage.disk.invalid"));
              return;
            }
            body[k] = n;
          }
          // Checked here too so an inverted pair reads as a sentence rather than as a rejected request.
          const warn = body.warn_percent ?? data.defaults.warn_percent;
          const high = body.high_percent ?? data.defaults.high_percent;
          if (warn >= high) {
            setInvalid(t("storage.disk.order"));
            return;
          }
          setInvalid(null);
          save.mutate(body);
        }}
      >
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
          {DISK_SPACE_LEVELS.map((k) => (
            <div key={k} className="flex min-w-0 flex-col gap-1.5 rounded-lg border p-3" data-testid={`disk-level-${k}`}>
              <Label htmlFor={`${id}-${k}`}>{t(`storage.disk.settings.${k}`)}</Label>
              <div className="flex flex-wrap items-baseline gap-2">
                <span className="text-lg font-semibold tabular-nums">{data.effective[k]}</span>
                <Badge variant="outline">{t(data.configured?.[k] == null ? "storage.disk.sources.default" : "storage.disk.sources.configured")}</Badge>
              </div>
              <p className="text-xs text-muted-foreground">{t("storage.disk.builtIn", { value: data.defaults[k] })}</p>
              {data.can_manage && (
                <Input
                  id={`${id}-${k}`}
                  inputMode="numeric"
                  placeholder={t("storage.disk.useDefault")}
                  value={draft[k]}
                  onChange={(e) => setDraft({ ...draft, [k]: e.target.value })}
                />
              )}
            </div>
          ))}
        </div>
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
    </SettingsSection>
  );
}
