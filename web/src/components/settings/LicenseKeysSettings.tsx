import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Eye, EyeOff, KeyRound, Loader2, Plus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { createLicenseKey, licenseKeysQuery, revokeLicenseKey } from "@/api/account";
import { CUSTOM_KEY_MAX, CUSTOM_KEY_MIN, customKeyProblem } from "@/api/licenseKeyValue";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { usePermissions } from "@/lib/org-writable";
import { cn } from "@/lib/utils";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SecretReveal } from "./SecretReveal";

type Created = { name: string; key: string | null };

export function LicenseKeysSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const canManage = usePermissions().can("license_keys.manage");
  const keys = useQuery(licenseKeysQuery());
  const [name, setName] = useState("");
  const [useOwn, setUseOwn] = useState(false);
  // An imported value lives only in this state until the key is created.
  const [value, setValue] = useState("");
  const [showValue, setShowValue] = useState(false);
  const [valueTouched, setValueTouched] = useState(false);
  // A generated plaintext key exists only in this state until "Done".
  const [created, setCreated] = useState<Created | null>(null);

  const trimmedValue = value.trim();
  const problem = useOwn ? customKeyProblem(trimmedValue) : null;
  const showProblem = useOwn && problem !== null && (valueTouched || trimmedValue !== "");
  const canSubmit = name.trim() !== "" && (!useOwn || problem === null);

  const invalidate = () => void qc.invalidateQueries({ queryKey: licenseKeysQuery().queryKey });
  const create = useMutation({
    mutationFn: (v: { name: string; key?: string }) => createLicenseKey(v.name, v.key),
    onSuccess: (res) => {
      setCreated({ name: res.license_key.name, key: res.key ?? null });
      setName("");
      setValue("");
      setShowValue(false);
      setValueTouched(false);
      setUseOwn(false);
      invalidate();
    },
  });
  const revoke = useMutation({ mutationFn: (keyId: string) => revokeLicenseKey(keyId), onSettled: invalidate });

  const problemText =
    problem === "chars"
      ? t("settings.licenseKeys.valueChars")
      : t("settings.licenseKeys.valueLength", { min: CUSTOM_KEY_MIN, max: CUSTOM_KEY_MAX, count: trimmedValue.length });

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("settings.licenseKeys.title")} description={t("settings.licenseKeys.description")}>
        {canManage && (
          <form
            data-testid="license-key-form"
            className="flex flex-col gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (!canSubmit) {
                setValueTouched(true);
                return;
              }
              setCreated(null);
              create.mutate(useOwn ? { name: name.trim(), key: trimmedValue } : { name: name.trim() });
            }}
          >
            <div className="flex flex-wrap items-end gap-2">
              <div className="flex min-w-0 flex-1 basis-60 flex-col gap-1.5">
                <Label htmlFor={`${id}-name`}>{t("settings.licenseKeys.name")}</Label>
                <Input id={`${id}-name`} value={name} maxLength={200} placeholder={t("settings.licenseKeys.namePlaceholder")} onChange={(e) => setName(e.target.value)} />
              </div>
              <Button type="submit" className="max-sm:w-full" disabled={create.isPending || !canSubmit}>
                {create.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Plus aria-hidden="true" />}
                {t("settings.licenseKeys.create")}
              </Button>
            </div>

            <label htmlFor={`${id}-own`} className="flex w-fit cursor-pointer items-center gap-2 text-sm">
              <button
                id={`${id}-own`}
                type="button"
                role="switch"
                aria-checked={useOwn}
                onClick={() => {
                  setUseOwn((on) => !on);
                  setValueTouched(false);
                }}
                className={cn(
                  "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 pointer-coarse:h-6 pointer-coarse:w-11",
                  useOwn ? "bg-primary" : "bg-input",
                )}
              >
                <span
                  aria-hidden="true"
                  className={cn(
                    "pointer-events-none block size-4 rounded-full bg-background shadow-sm transition-transform pointer-coarse:size-5",
                    useOwn ? "translate-x-4 pointer-coarse:translate-x-5" : "translate-x-0.5",
                  )}
                />
              </button>
              {t("settings.licenseKeys.useOwn")}
            </label>

            {useOwn && (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${id}-value`}>{t("settings.licenseKeys.value")}</Label>
                <div className="flex gap-2">
                  <Input
                    id={`${id}-value`}
                    type={showValue ? "text" : "password"}
                    value={value}
                    maxLength={CUSTOM_KEY_MAX + 64}
                    autoComplete="off"
                    autoCapitalize="off"
                    autoCorrect="off"
                    spellCheck={false}
                    placeholder={t("settings.licenseKeys.valuePlaceholder")}
                    className="font-mono"
                    aria-invalid={showProblem || undefined}
                    aria-describedby={`${id}-value-help${showProblem ? ` ${id}-value-error` : ""}`}
                    onChange={(e) => setValue(e.target.value)}
                    onBlur={() => setValueTouched(true)}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    className="shrink-0"
                    aria-controls={`${id}-value`}
                    aria-pressed={showValue}
                    aria-label={showValue ? t("settings.licenseKeys.hideValue") : t("settings.licenseKeys.showValue")}
                    onClick={() => setShowValue((s) => !s)}
                  >
                    {showValue ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
                  </Button>
                </div>
                {showProblem && (
                  <p id={`${id}-value-error`} className="text-xs text-destructive-text" role="alert">
                    {problemText}
                  </p>
                )}
                <p id={`${id}-value-help`} className="text-xs text-muted-foreground">
                  {t("settings.licenseKeys.valueHelp")}
                </p>
              </div>
            )}
          </form>
        )}
        <FormError error={create.error ?? revoke.error} />
        {created &&
          (created.key !== null ? (
            <SecretReveal
              label={t("settings.licenseKeys.created", { name: created.name })}
              secret={created.key}
              note={t("settings.licenseKeys.usage")}
              onDone={() => setCreated(null)}
            />
          ) : (
            <div data-testid="license-key-imported" role="status" className="flex flex-col gap-2 rounded-lg border border-success/50 bg-success/10 p-3">
              <p className="flex items-center gap-2 text-sm font-medium">
                <CheckCircle2 className="size-4 shrink-0 text-success-text" aria-hidden="true" />
                {t("settings.licenseKeys.importedTitle", { name: created.name })}
              </p>
              <p className="text-xs text-muted-foreground">{t("settings.licenseKeys.importedNote")}</p>
              <div>
                <Button type="button" size="sm" onClick={() => setCreated(null)}>
                  {t("settings.done")}
                </Button>
              </div>
            </div>
          ))}
        <p className="text-xs text-muted-foreground">{t("settings.licenseKeys.revokeNote")}</p>
      </SettingsSection>

      <div className="rounded-xl border bg-card">
        {keys.isPending ? (
          <LoadingState />
        ) : keys.isError ? (
          <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
        ) : keys.data.length === 0 ? (
          <EmptyState icon={<KeyRound className="size-5" aria-hidden="true" />}>{t("settings.licenseKeys.empty")}</EmptyState>
        ) : (
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("settings.columns.name")}</TableHead>
                <TableHead>{t("settings.columns.key")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("settings.columns.createdBy")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.columns.created")}</TableHead>
                <TableHead>{t("settings.columns.lastUsed")}</TableHead>
                <TableHead>{t("settings.columns.status")}</TableHead>
                {canManage && (
                  <TableHead>
                    <span className="sr-only">{t("settings.columns.actions")}</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.data.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell label={t("settings.columns.key")}>
                    <span className="inline-flex flex-wrap items-center gap-1.5">
                      <span>
                        <code className="font-mono text-xs">{k.prefix}</code>
                        <span aria-hidden="true">…</span>
                      </span>
                      {k.custom && (
                        <Badge variant="outline" title={t("settings.licenseKeys.customHint")}>
                          {t("settings.licenseKeys.custom")}
                        </Badge>
                      )}
                    </span>
                  </TableCell>
                  <TableCell label={t("settings.columns.createdBy")} className="hidden break-all lg:table-cell">
                    {k.created_by_email || "–"}
                  </TableCell>
                  <TableCell label={t("settings.columns.created")} className="hidden md:table-cell">
                    <DateTimeText value={k.created_at} />
                  </TableCell>
                  <TableCell label={t("settings.columns.lastUsed")}>
                    <DateTimeText value={k.last_used_at} relative />
                  </TableCell>
                  <TableCell className="max-md:w-auto">
                    {k.revoked_at ? <Badge variant="muted">{t("settings.revoked")}</Badge> : <Badge variant="success">{t("settings.active")}</Badge>}
                  </TableCell>
                  {canManage && (
                    <TableCell className="text-right">
                      {!k.revoked_at && (
                        <ConfirmButton
                          label={t("settings.revoke")}
                          confirmLabel={t("settings.confirmRevoke")}
                          pending={revoke.isPending && revoke.variables === k.id}
                          onConfirm={() => revoke.mutate(k.id)}
                        />
                      )}
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}
