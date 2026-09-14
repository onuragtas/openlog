import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  deleteQueryLimits,
  putQueryLimits,
  QUERY_LIMIT_SETTINGS,
  queryLimitsQuery,
  type OrgQueryLimits,
  type OrgQueryLimitsInput,
  type QueryLimitSetting,
} from "@/api/usage";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { formatBytes, formatNumber } from "@/lib/format";
import { FormError, SettingsSection } from "./common";

function formatLimit(setting: QueryLimitSetting, v: number, locale: string, unlimited: string): string {
  if (v <= 0) return unlimited;
  return setting === "max_rows_to_read" ? formatNumber(v, locale) : formatBytes(v);
}

type Draft = Record<QueryLimitSetting, string>;

function draftOf(l: OrgQueryLimits): Draft {
  const org = l.organization;
  const v = (s: QueryLimitSetting) => {
    const x = org?.[s];
    return x === null || x === undefined ? "" : String(x);
  };
  return { max_memory_usage: v("max_memory_usage"), max_rows_to_read: v("max_rows_to_read"), max_bytes_to_read: v("max_bytes_to_read") };
}

/** Settings → Usage & plan → Query limits: effective ClickHouse limits per layer, editable organization layer. */
export function QueryLimitsSettings() {
  const q = useQuery(queryLimitsQuery());
  if (q.isLoading) return <LoadingState />;
  if (q.error) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return null;
  // Not keyed on the data: a save must not remount the form (it would drop the "saved" message).
  return <QueryLimitsForm limits={q.data} />;
}

function QueryLimitsForm({ limits }: { limits: OrgQueryLimits }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const qc = useQueryClient();
  const [draft, setDraft] = useState<Draft>(() => draftOf(limits));
  const [invalid, setInvalid] = useState(false);
  const onDone = (data: OrgQueryLimits) => {
    qc.setQueryData(queryLimitsQuery().queryKey, data);
    setDraft(draftOf(data));
  };
  const save = useMutation({ mutationFn: putQueryLimits, onSuccess: onDone });
  const reset = useMutation({ mutationFn: deleteQueryLimits, onSuccess: onDone });
  const unlimited = t("usage.queryLimits.unlimited");

  return (
    <SettingsSection title={t("usage.queryLimits.title")} description={t("usage.queryLimits.description")}>
      {limits.environment && (
        <p role="note" className="rounded-md border px-3 py-2 text-sm text-muted-foreground">
          {t("usage.queryLimits.environmentNote")}
        </p>
      )}
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          const body: OrgQueryLimitsInput = {};
          for (const s of QUERY_LIMIT_SETTINGS) {
            const raw = draft[s].trim();
            if (raw === "") {
              body[s] = null;
              continue;
            }
            const n = Number(raw);
            if (!Number.isSafeInteger(n) || n < 0) {
              setInvalid(true);
              return;
            }
            body[s] = n;
          }
          setInvalid(false);
          save.mutate(body);
        }}
      >
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
          {QUERY_LIMIT_SETTINGS.map((s) => (
            <div key={s} className="flex min-w-0 flex-col gap-1.5 rounded-lg border p-3" data-testid={`query-limit-${s}`}>
              <Label htmlFor={`${id}-${s}`}>{t(`usage.queryLimits.settings.${s}`)}</Label>
              <div className="flex flex-wrap items-baseline gap-2">
                <span className="text-lg font-semibold tabular-nums">{formatLimit(s, limits.effective[s], locale, unlimited)}</span>
                <Badge variant="outline">{t(`usage.queryLimits.sources.${limits.sources[s]}`)}</Badge>
              </div>
              <p className="text-xs text-muted-foreground">
                {t("usage.queryLimits.layers", {
                  defaults: formatLimit(s, limits.defaults[s], locale, unlimited),
                  plan: limits.plan[s] > 0 ? formatLimit(s, limits.plan[s], locale, unlimited) : "—",
                })}
              </p>
              {limits.can_manage && (
                <Input
                  id={`${id}-${s}`}
                  inputMode="numeric"
                  placeholder={t("usage.queryLimits.inherit")}
                  value={draft[s]}
                  onChange={(e) => setDraft({ ...draft, [s]: e.target.value })}
                />
              )}
            </div>
          ))}
        </div>
        {invalid && (
          <p role="alert" className="text-sm text-destructive">
            {t("usage.queryLimits.invalid")}
          </p>
        )}
        {limits.can_manage ? (
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" disabled={save.isPending || reset.isPending}>
              {t("usage.queryLimits.save")}
            </Button>
            {limits.organization && (
              <Button type="button" variant="outline" disabled={save.isPending || reset.isPending} onClick={() => reset.mutate()}>
                {t("usage.queryLimits.reset")}
              </Button>
            )}
            {(save.isSuccess || reset.isSuccess) && (
              <span className="text-sm text-muted-foreground">{t("usage.queryLimits.saved", { seconds: limits.refresh_seconds })}</span>
            )}
            <FormError error={save.error ?? reset.error} />
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">{t("usage.queryLimits.readOnly")}</p>
        )}
        <p className="text-xs text-muted-foreground">{t("usage.queryLimits.hint")}</p>
      </form>
    </SettingsSection>
  );
}
