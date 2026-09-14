import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CheckCircle2, Eye, EyeOff, Loader2, Plus } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { createLicenseKey, licenseKeysQuery } from "@/api/account";
import type { Onboarding } from "@/api/onboarding";
import { FormError } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { pastedKeyProblem, type KeyChoice, type KeyMode } from "@/lib/onboarding-key";
import { cn } from "@/lib/utils";

function Choice({
  name,
  mode,
  current,
  disabled,
  label,
  help,
  onSelect,
  children,
}: {
  name: string;
  mode: KeyMode;
  current: KeyMode;
  disabled?: boolean;
  label: string;
  help: string;
  onSelect: (m: KeyMode) => void;
  children?: ReactNode;
}) {
  const id = `${name}-${mode}`;
  const checked = current === mode;
  return (
    <div className={cn("rounded-lg border p-3", checked && "border-primary bg-primary/5", disabled && "opacity-70")}>
      <label htmlFor={id} className={cn("flex items-start gap-3", !disabled && "cursor-pointer")}>
        <input
          id={id}
          type="radio"
          name={name}
          value={mode}
          checked={checked}
          disabled={disabled}
          onChange={() => onSelect(mode)}
          className="mt-0.5 size-4 shrink-0 accent-primary pointer-coarse:size-5"
          aria-describedby={`${id}-help`}
        />
        <span className="min-w-0">
          <span className="block text-sm font-medium">{label}</span>
          <span id={`${id}-help`} className="block text-xs text-muted-foreground">
            {help}
          </span>
        </span>
      </label>
      {checked && children && <div className="mt-3 flex min-w-0 flex-col gap-2 sm:pl-7">{children}</div>}
    </div>
  );
}

/**
 * Step 1: which license key goes into the commands. Admins create one inline (value shown once); anyone can paste a
 * key they have (kept in this tab only) or use the <LICENSE_KEY> placeholder.
 */
export function LicenseKeyStep({
  onboarding,
  targetTitle,
  needed,
  value,
  onChange,
}: {
  onboarding: Onboarding;
  targetTitle: string;
  needed: boolean;
  value: KeyChoice;
  onChange: (v: KeyChoice) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const canCreate = onboarding.features.can_create_license_keys;
  const canList = onboarding.features.can_list_license_keys;
  const keys = useQuery({ ...licenseKeysQuery(), enabled: needed && canList });
  const active = (keys.data ?? []).filter((k) => !k.revoked_at);
  const [name, setName] = useState<string>(() => t("addData.key.defaultName", { target: targetTitle, date: new Date().toISOString().slice(0, 10) }));
  const [show, setShow] = useState(false);
  const create = useMutation({
    mutationFn: (keyName: string) => createLicenseKey(keyName),
    onSuccess: (res) => {
      if (res.key) onChange({ ...value, mode: "create", created: { name: res.license_key.name, key: res.key } });
      void qc.invalidateQueries({ queryKey: licenseKeysQuery().queryKey });
    },
  });

  if (!needed) {
    return (
      <p data-testid="key-not-needed" className="rounded-lg border bg-muted/40 p-3 text-sm">
        {t("addData.key.notNeeded")}
      </p>
    );
  }

  const select = (mode: KeyMode) => onChange({ ...value, mode });
  const selected = active.find((k) => k.id === value.pasteFor);
  const pasted = value.pasted.trim();
  const charsProblem = pastedKeyProblem(pasted) !== null;
  const mismatch = !!selected && pasted !== "" && !charsProblem && !pasted.startsWith(selected.prefix);

  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">{t("addData.key.description")}</p>
      <fieldset className="flex min-w-0 flex-col gap-3">
        <legend className="sr-only">{t("addData.key.title")}</legend>
        <Choice
          name={`${id}-mode`}
          mode="create"
          current={value.mode}
          disabled={!canCreate}
          label={t("addData.key.create")}
          help={canCreate ? t("addData.key.createHelp") : t("addData.key.createDisabled")}
          onSelect={select}
        >
          {value.created ? (
            <div data-testid="key-created" role="status" className="flex items-start gap-2 rounded-md border border-success/50 bg-success/10 p-2 text-sm">
              <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success-text" aria-hidden="true" />
              <span>{t("addData.key.created", { name: value.created.name })}</span>
            </div>
          ) : (
            <form
              className="flex flex-wrap items-end gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                if (name.trim()) create.mutate(name.trim());
              }}
            >
              <div className="flex min-w-0 flex-1 basis-56 flex-col gap-1.5">
                <Label htmlFor={`${id}-name`}>{t("addData.key.name")}</Label>
                <Input id={`${id}-name`} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} />
              </div>
              <Button type="submit" className="max-sm:w-full" disabled={create.isPending || !name.trim()}>
                {create.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Plus aria-hidden="true" />}
                {t("addData.key.createButton")}
              </Button>
            </form>
          )}
          <FormError error={create.error} />
        </Choice>

        <Choice name={`${id}-mode`} mode="paste" current={value.mode} label={t("addData.key.have")} help={t("addData.key.haveHelp")} onSelect={select}>
          {active.length > 0 && (
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-for`}>{t("addData.key.existing")}</Label>
              <NativeSelect id={`${id}-for`} className="w-full sm:w-auto" value={value.pasteFor} onChange={(e) => onChange({ ...value, pasteFor: e.target.value })}>
                <option value="">{t("addData.key.anyKey")}</option>
                {active.map((k) => (
                  <option key={k.id} value={k.id}>
                    {k.name} ({k.prefix}…)
                  </option>
                ))}
              </NativeSelect>
            </div>
          )}
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-value`}>{t("addData.key.value")}</Label>
            <div className="flex min-w-0 gap-2">
              <Input
                id={`${id}-value`}
                type={show ? "text" : "password"}
                value={value.pasted}
                autoComplete="off"
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
                className="min-w-0 flex-1 font-mono"
                aria-invalid={charsProblem || mismatch || undefined}
                aria-describedby={charsProblem || mismatch ? `${id}-value-error` : undefined}
                onChange={(e) => onChange({ ...value, pasted: e.target.value })}
              />
              <Button
                type="button"
                variant="outline"
                size="icon"
                className="shrink-0"
                aria-controls={`${id}-value`}
                aria-pressed={show}
                aria-label={show ? t("addData.key.hide") : t("addData.key.show")}
                onClick={() => setShow((s) => !s)}
              >
                {show ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
              </Button>
            </div>
            {(charsProblem || mismatch) && (
              <p id={`${id}-value-error`} role="alert" className="text-xs text-destructive-text">
                {charsProblem ? t("addData.key.invalidChars") : t("addData.key.prefixMismatch", { prefix: selected!.prefix, name: selected!.name })}
              </p>
            )}
          </div>
        </Choice>

        <Choice name={`${id}-mode`} mode="placeholder" current={value.mode} label={t("addData.key.placeholder")} help={t("addData.key.placeholderHelp")} onSelect={select} />
      </fieldset>
      {canList && (
        <Link to="/settings/license-keys" className="w-fit text-xs text-primary hover:underline">
          {t("addData.key.manage")}
        </Link>
      )}
    </div>
  );
}
