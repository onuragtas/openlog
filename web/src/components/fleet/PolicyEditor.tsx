import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Loader2, Plus, Save, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  saveFleetPolicy,
  type FleetChannel,
  type FleetMode,
  type FleetPHPAgentMode,
  type FleetPolicy,
  type FleetPolicyInput,
  type FleetTarget,
  type MaintenanceWindow,
} from "@/api/fleet";
import { DateTimeText, FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { formatWaves, isVersion, parseWaves, WEEKDAYS, windowError } from "@/lib/fleet";
import { cn } from "@/lib/utils";

interface FormState {
  mode: FleetMode;
  channel: FleetChannel;
  target: FleetTarget;
  pinned: string;
  waves: string;
  soak: string;
  haltPct: string;
  windows: MaintenanceWindow[];
  phpMode: FleetPHPAgentMode;
  phpVersion: string;
  phpReload: "none" | "graceful";
  phpExclude: string;
}

const PHP_MODES: FleetPHPAgentMode[] = ["off", "manual", "auto"];

const excludeLines = (text: string) =>
  text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

function toForm(p: FleetPolicy): FormState {
  return {
    phpMode: p.php_agent.mode,
    phpVersion: p.php_agent.version,
    phpReload: p.php_agent.reload,
    phpExclude: p.php_agent.exclude_bins.join("\n"),
    mode: p.mode,
    channel: p.channel,
    target: p.target,
    pinned: p.pinned_version ?? "",
    waves: formatWaves(p.waves),
    soak: String(p.wave_soak_minutes),
    haltPct: String(Math.round(p.halt_failure_rate * 1000) / 10),
    windows: p.maintenance_windows.map((w) => ({ ...w, days: [...w.days] })),
  };
}

const MODES: FleetMode[] = ["off", "notify", "auto"];

/** Update policy form. Viewers see it read-only. Keyed by the parent on the stored policy. */
export function PolicyEditor({ policy, canManage }: { policy: FleetPolicy; canManage: boolean }) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const [form, setForm] = useState<FormState>(() => toForm(policy));
  const [saved, setSaved] = useState(false);
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => {
    setSaved(false);
    setForm((f) => ({ ...f, [key]: value }));
  };

  const waves = parseWaves(form.waves);
  const soak = Number(form.soak);
  const soakOk = /^\d+$/.test(form.soak.trim()) && soak <= 43200;
  const halt = Number(form.haltPct);
  const haltOk = form.haltPct.trim() !== "" && Number.isFinite(halt) && halt >= 0 && halt <= 100;
  const pinnedOk = form.target !== "pinned" || isVersion(form.pinned);
  const windowErrors = form.windows.map((w) => windowError(w));
  const phpVersionOk = form.phpVersion.trim() === "agent" || isVersion(form.phpVersion);
  const phpExcludeOk = excludeLines(form.phpExclude).length <= 50;
  const valid = !waves.error && soakOk && haltOk && pinnedOk && windowErrors.every((e) => e === null) && phpVersionOk && phpExcludeOk;
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(policy));

  const save = useMutation({
    mutationFn: (input: FleetPolicyInput) => saveFleetPolicy(input),
    onSuccess: (stored) => {
      qc.setQueryData(["fleet", "policy"], stored);
      setForm(toForm(stored));
      setSaved(true);
      void qc.invalidateQueries({ queryKey: ["fleet"] });
    },
  });

  const submit = () => {
    if (!valid) return;
    save.mutate({
      mode: form.mode,
      channel: form.channel,
      target: form.target,
      pinned_version: form.target === "pinned" ? form.pinned.trim() : null,
      waves: waves.waves,
      wave_soak_minutes: soak,
      halt_failure_rate: halt / 100,
      maintenance_windows: form.windows,
      php_agent: { mode: form.phpMode, version: form.phpVersion.trim(), reload: form.phpReload, exclude_bins: excludeLines(form.phpExclude) },
    });
  };

  const setWindow = (i: number, w: MaintenanceWindow) => set("windows", form.windows.map((x, j) => (j === i ? w : x)));

  return (
    <form
      className="flex flex-col gap-5"
      aria-labelledby={`${id}-title`}
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div>
        <h2 id={`${id}-title`} className="text-base font-semibold">
          {t("fleet.policy.title")}
        </h2>
        <p className="text-sm text-muted-foreground">{t("fleet.policy.description")}</p>
        <p className="mt-1 text-xs text-muted-foreground">
          {policy.is_default ? (
            t("fleet.policy.default")
          ) : (
            <>
              {t("fleet.policy.updatedBy", { email: policy.updated_by_email || "–" })} <DateTimeText value={policy.updated_at} relative />
            </>
          )}
        </p>
        {!canManage && <p className="mt-1 text-xs text-muted-foreground">{t("fleet.readOnly")}</p>}
      </div>

      {/* min-w-0: a fieldset is at least as wide as its content by default (long select options on phones). */}
      <fieldset disabled={!canManage || save.isPending} className="flex min-w-0 flex-col gap-5">
        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 text-sm font-medium">{t("fleet.policy.mode")}</legend>
          <div className="grid gap-2 sm:grid-cols-3">
            {MODES.map((m) => (
              <label
                key={m}
                className={cn(
                  "flex cursor-pointer flex-col gap-0.5 rounded-lg border px-3 py-2 text-sm has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring",
                  form.mode === m && "border-primary bg-primary/5",
                )}
              >
                <span className="flex items-center gap-2 font-medium">
                  <input type="radio" name={`${id}-mode`} value={m} checked={form.mode === m} onChange={() => set("mode", m)} className="accent-primary" />
                  {t(`fleet.policy.modes.${m}`)}
                </span>
                <span className="text-xs text-muted-foreground">{t(`fleet.policy.modeHelp.${m}`)}</span>
              </label>
            ))}
          </div>
        </fieldset>

        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-channel`}>{t("fleet.policy.channel")}</Label>
            <NativeSelect id={`${id}-channel`} value={form.channel} onChange={(e) => set("channel", e.target.value as FleetChannel)}>
              <option value="stable">{t("fleet.policy.channels.stable")}</option>
              <option value="beta">{t("fleet.policy.channels.beta")}</option>
            </NativeSelect>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-target`}>{t("fleet.policy.target")}</Label>
            <NativeSelect id={`${id}-target`} value={form.target} onChange={(e) => set("target", e.target.value as FleetTarget)}>
              <option value="latest">{t("fleet.policy.targets.latest")}</option>
              <option value="patch">{t("fleet.policy.targets.patch")}</option>
              <option value="pinned">{t("fleet.policy.targets.pinned")}</option>
            </NativeSelect>
          </div>
          {form.target === "pinned" && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-pinned`}>{t("fleet.policy.pinnedVersion")}</Label>
              <Input
                id={`${id}-pinned`}
                value={form.pinned}
                placeholder="0.4.0"
                aria-invalid={!pinnedOk}
                aria-describedby={pinnedOk ? undefined : `${id}-pinned-error`}
                onChange={(e) => set("pinned", e.target.value)}
              />
              {!pinnedOk && (
                <p id={`${id}-pinned-error`} className="text-xs text-destructive-text">
                  {t("fleet.policy.pinnedError")}
                </p>
              )}
            </div>
          )}
        </div>

        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-waves`}>{t("fleet.policy.waves")}</Label>
            <Input
              id={`${id}-waves`}
              value={form.waves}
              inputMode="numeric"
              aria-invalid={waves.error !== null}
              aria-describedby={`${id}-waves-help`}
              onChange={(e) => set("waves", e.target.value)}
            />
            <p id={`${id}-waves-help`} className={cn("text-xs", waves.error ? "text-destructive-text" : "text-muted-foreground")}>
              {waves.error ? t(`fleet.policy.wavesErrors.${waves.error}`) : t("fleet.policy.wavesHelp")}
            </p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-soak`}>{t("fleet.policy.soak")}</Label>
            <Input
              id={`${id}-soak`}
              type="number"
              min={0}
              max={43200}
              value={form.soak}
              aria-invalid={!soakOk}
              aria-describedby={`${id}-soak-help`}
              onChange={(e) => set("soak", e.target.value)}
            />
            <p id={`${id}-soak-help`} className={cn("text-xs", soakOk ? "text-muted-foreground" : "text-destructive-text")}>
              {soakOk ? t("fleet.policy.soakHelp") : t("fleet.policy.soakError")}
            </p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-halt`}>{t("fleet.policy.haltRate")}</Label>
            <Input
              id={`${id}-halt`}
              type="number"
              min={0}
              max={100}
              step={0.5}
              value={form.haltPct}
              aria-invalid={!haltOk}
              aria-describedby={`${id}-halt-help`}
              onChange={(e) => set("haltPct", e.target.value)}
            />
            <p id={`${id}-halt-help`} className={cn("text-xs", haltOk ? "text-muted-foreground" : "text-destructive-text")}>
              {haltOk ? t("fleet.policy.haltRateHelp") : t("fleet.policy.haltRateError")}
            </p>
          </div>
        </div>

        <fieldset className="flex flex-col gap-2">
          <legend className="text-sm font-medium">{t("fleet.policy.windows")}</legend>
          <p className="text-xs text-muted-foreground">{t("fleet.policy.windowsHelp")}</p>
          {form.windows.map((w, i) => {
            const err = windowErrors[i];
            return (
              <div key={i} role="group" aria-label={t("fleet.policy.windowLabel", { n: i + 1 })} className="flex flex-wrap items-end gap-3 rounded-lg border px-3 py-2">
                <div className="flex flex-col gap-1">
                  <span className="text-xs font-medium">{t("fleet.policy.days")}</span>
                  <div className="flex flex-wrap gap-1">
                    {WEEKDAYS.map((d) => {
                      const on = w.days.includes(d);
                      return (
                        <Button
                          key={d}
                          type="button"
                          size="sm"
                          variant={on ? "default" : "outline"}
                          aria-pressed={on}
                          className="h-7 px-2"
                          onClick={() => setWindow(i, { ...w, days: on ? w.days.filter((x) => x !== d) : WEEKDAYS.filter((x) => x === d || w.days.includes(x)) })}
                        >
                          {t(`fleet.policy.weekdays.${d}`)}
                        </Button>
                      );
                    })}
                  </div>
                  {w.days.length === 0 && <span className="text-xs text-muted-foreground">{t("fleet.policy.everyDay")}</span>}
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor={`${id}-w${i}-start`} className="text-xs">
                    {t("fleet.policy.start")}
                  </Label>
                  <Input id={`${id}-w${i}-start`} className="w-28" value={w.start} placeholder="02:00" aria-invalid={err !== null} onChange={(e) => setWindow(i, { ...w, start: e.target.value })} />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor={`${id}-w${i}-end`} className="text-xs">
                    {t("fleet.policy.end")}
                  </Label>
                  <Input id={`${id}-w${i}-end`} className="w-28" value={w.end} placeholder="05:00" aria-invalid={err !== null} onChange={(e) => setWindow(i, { ...w, end: e.target.value })} />
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t("fleet.policy.removeWindow", { n: i + 1 })}
                  onClick={() => set("windows", form.windows.filter((_, j) => j !== i))}
                >
                  <Trash2 aria-hidden="true" />
                </Button>
                {err && <p className="basis-full text-xs text-destructive-text">{t(`fleet.policy.windowErrors.${err}`)}</p>}
              </div>
            );
          })}
          <div>
            <Button type="button" variant="outline" size="sm" onClick={() => set("windows", [...form.windows, { days: ["sat", "sun"], start: "02:00", end: "05:00" }])}>
              <Plus aria-hidden="true" />
              {t("fleet.policy.addWindow")}
            </Button>
          </div>
        </fieldset>

        <fieldset className="flex min-w-0 flex-col gap-3 border-t pt-4" aria-describedby={`${id}-php-desc`}>
          <legend className="text-sm font-medium">{t("fleet.policy.php.title")}</legend>
          <p id={`${id}-php-desc`} className="text-xs text-muted-foreground">
            {t("fleet.policy.php.description")}
          </p>
          <div className="grid grid-cols-[minmax(0,1fr)] gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-php-mode`}>{t("fleet.policy.php.mode")}</Label>
              <NativeSelect id={`${id}-php-mode`} className="w-full min-w-0" value={form.phpMode} aria-describedby={`${id}-php-mode-help`} onChange={(e) => set("phpMode", e.target.value as FleetPHPAgentMode)}>
                {PHP_MODES.map((m) => (
                  <option key={m} value={m}>
                    {t(`fleet.policy.php.modes.${m}`)}
                  </option>
                ))}
              </NativeSelect>
              <p id={`${id}-php-mode-help`} className="text-xs text-muted-foreground">
                {t(`fleet.policy.php.modeHelp.${form.phpMode}`)}
              </p>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-php-version`}>{t("fleet.policy.php.version")}</Label>
              <Input
                id={`${id}-php-version`}
                value={form.phpVersion}
                placeholder="agent"
                aria-invalid={!phpVersionOk}
                aria-describedby={`${id}-php-version-help`}
                onChange={(e) => set("phpVersion", e.target.value)}
              />
              <p id={`${id}-php-version-help`} className={cn("text-xs", phpVersionOk ? "text-muted-foreground" : "text-destructive-text")}>
                {phpVersionOk ? t("fleet.policy.php.versionHelp") : t("fleet.policy.php.versionError")}
              </p>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-php-reload`}>{t("fleet.policy.php.reload")}</Label>
              <NativeSelect id={`${id}-php-reload`} className="w-full min-w-0" value={form.phpReload} onChange={(e) => set("phpReload", e.target.value as FormState["phpReload"])}>
                <option value="none">{t("fleet.policy.php.reloads.none")}</option>
                <option value="graceful">{t("fleet.policy.php.reloads.graceful")}</option>
              </NativeSelect>
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-php-exclude`}>{t("fleet.policy.php.excludeBins")}</Label>
            <textarea
              id={`${id}-php-exclude`}
              rows={2}
              value={form.phpExclude}
              placeholder="/usr/bin/php7.4"
              spellCheck={false}
              aria-invalid={!phpExcludeOk}
              aria-describedby={`${id}-php-exclude-help`}
              onChange={(e) => set("phpExclude", e.target.value)}
              className="w-full max-w-xl rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 aria-invalid:border-destructive"
            />
            <p id={`${id}-php-exclude-help`} className={cn("text-xs", phpExcludeOk ? "text-muted-foreground" : "text-destructive-text")}>
              {phpExcludeOk ? t("fleet.policy.php.excludeBinsHelp") : t("fleet.policy.php.excludeBinsError")}
            </p>
          </div>
        </fieldset>
      </fieldset>

      <FormError error={save.error} />
      {canManage && (
        <div className="flex flex-wrap items-center gap-2">
          <Button type="submit" disabled={!valid || !dirty || save.isPending}>
            {save.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Save aria-hidden="true" />}
            {t("fleet.policy.save")}
          </Button>
          <Button type="button" variant="ghost" disabled={!dirty || save.isPending} onClick={() => setForm(toForm(policy))}>
            {t("fleet.policy.reset")}
          </Button>
          {saved && (
            <span role="status" className="text-sm text-success-text">
              {t("fleet.policy.saved")}
            </span>
          )}
        </div>
      )}
    </form>
  );
}
