import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil } from "lucide-react";
import { useId, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { apmSettingsQuery, putApmSettings } from "@/api/apm";
import { ApiError } from "@/api/client";
import { atLeast } from "@/api/roles";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { formatMs, type ServiceScope } from "@/lib/apm";

/** Apdex T of a service with an inline editor for signed-in admins (PUT …/settings). */
export function ApdexSettings({ scope }: { scope: ServiceScope }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const me = useMe();
  const qc = useQueryClient();
  const settings = useQuery(apmSettingsQuery(scope));
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [invalid, setInvalid] = useState(false);
  const canEdit = me.data?.auth === "session" && atLeast(me.data?.role, "admin");
  const save = useMutation({
    mutationFn: (ms: number) => putApmSettings(scope, ms),
    onSuccess: () => {
      setEditing(false);
      void qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0]).startsWith("apm-") });
    },
  });

  if (!settings.data) return null;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const n = Number(value);
    if (!Number.isInteger(n) || n < 1 || n > 600000) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    save.mutate(n);
  };

  if (editing) {
    return (
      <form onSubmit={submit} noValidate className="flex flex-wrap items-end gap-2" aria-label={t("apm.settings.edit")}>
        <div className="flex flex-col gap-1">
          <Label htmlFor={id}>{t("apm.settings.apdexT")}</Label>
          <Input id={id} type="number" min={1} max={600000} step={1} className="h-8 w-28" value={value} onChange={(e) => setValue(e.target.value)} aria-invalid={invalid} autoFocus />
        </div>
        <Button type="submit" size="sm" disabled={save.isPending}>
          {t("apm.settings.save")}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(false)}>
          {t("apm.settings.cancel")}
        </Button>
        {(invalid || save.error) && (
          <p role="alert" className="w-full text-xs text-destructive-text">
            {invalid ? t("apm.settings.invalid") : save.error instanceof ApiError ? save.error.message : String(save.error)}
          </p>
        )}
      </form>
    );
  }
  return (
    <div className="flex items-center gap-1.5 text-xs" data-testid="apdex-settings">
      <span className="text-muted-foreground">{t("apm.metrics.apdex")}</span>
      <span className="font-mono">{t("apm.apdexT", { value: formatMs(settings.data.apdex_t_ms, locale) })}</span>
      {settings.data.is_default && <span className="text-muted-foreground">({t("apm.settings.isDefault")})</span>}
      {canEdit && (
        <Button
          variant="ghost"
          size="icon"
          className="size-6"
          aria-label={t("apm.settings.edit")}
          onClick={() => {
            setValue(String(settings.data.apdex_t_ms));
            setEditing(true);
          }}
        >
          <Pencil className="size-3" aria-hidden="true" />
        </Button>
      )}
    </div>
  );
}
