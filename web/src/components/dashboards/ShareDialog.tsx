import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Link2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Dashboard } from "@/api/dashboards";
import {
  createDashboardShare,
  dashboardSettingsQuery,
  dashboardSharesQuery,
  revokeDashboardShare,
  shareUrl,
  updateDashboardSettings,
  type DashboardShare,
  type DashboardShareCreated,
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
import { isCustomRange, PRESET_RANGES, resolveRange, type RangeSpec } from "@/lib/time";

export interface ShareDialogProps {
  dashboard: Dashboard;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  range: RangeSpec;
  vars: VarValues | undefined;
}

const parseTs = (s: string) => Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));
const SHARE_EXPIRY_DAYS = [1, 7, 30, 90] as const;
const expiresAt = (days: number) => new Date(Date.now() + days * 86_400_000).toISOString();

/** Read-only share links of a dashboard (api.md "Share links"). */
export function ShareDialog({ dashboard, open, onOpenChange, range, vars }: ShareDialogProps) {
  const { t } = useTranslation();
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" title={t("dashboards.share.title")} closeLabel={t("common.close")} className="w-full max-w-full sm:max-w-xl">
        {open && <ShareBody dashboard={dashboard} range={range} vars={vars} />}
      </SheetContent>
    </Sheet>
  );
}

function shareStatus(s: DashboardShare, now: number): "active" | "expired" | "revoked" {
  if (s.revoked_at) return "revoked";
  return parseTs(s.expires_at) <= now ? "expired" : "active";
}

function ShareBody({ dashboard, range, vars }: { dashboard: Dashboard; range: RangeSpec; vars: VarValues | undefined }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const queryClient = useQueryClient();
  const settings = useQuery(dashboardSettingsQuery());
  const shares = useQuery(dashboardSharesQuery(dashboard.id));
  const [created, setCreated] = useState<DashboardShareCreated | null>(null);
  const [now] = useState(() => Date.now());

  const toggleSharing = useMutation({
    mutationFn: (enabled: boolean) => updateDashboardSettings({ share_links_enabled: enabled, report_domains: settings.data?.report_domains ?? [] }),
    onSuccess: (st) => {
      queryClient.setQueryData(dashboardSettingsQuery().queryKey, st);
      void queryClient.invalidateQueries({ queryKey: ["dashboards", "shares", dashboard.id] });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) => revokeDashboardShare(dashboard.id, id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["dashboards", "shares", dashboard.id] }),
  });

  if (settings.isPending || shares.isPending) return <LoadingState />;
  if (settings.isError) return <ErrorState error={settings.error} onRetry={() => void settings.refetch()} />;
  if (shares.isError) return <ErrorState error={shares.error} onRetry={() => void shares.refetch()} />;
  const enabled = settings.data.share_links_enabled;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto p-4" data-testid="share-dialog">
      <p className="text-sm text-muted-foreground">{t("dashboards.share.hint")}</p>

      {!enabled && (
        <div role="status" className="flex flex-col gap-2 rounded-lg border border-warning/60 bg-warning/10 p-3 text-sm" data-testid="share-disabled">
          <p>{t("dashboards.share.disabled")}</p>
          {settings.data.can_edit ? (
            <Button className="w-fit" onClick={() => toggleSharing.mutate(true)} disabled={toggleSharing.isPending} data-testid="share-enable">
              {t("dashboards.share.enable")}
            </Button>
          ) : (
            <p className="text-muted-foreground">{t("dashboards.share.askAdmin")}</p>
          )}
        </div>
      )}

      {enabled && dashboard.can_edit && (
        <CreateShareForm
          dashboard={dashboard}
          range={range}
          vars={vars}
          onCreated={(s) => {
            setCreated(s);
            void queryClient.invalidateQueries({ queryKey: ["dashboards", "shares", dashboard.id] });
          }}
        />
      )}

      {created && <CreatedLink share={created} />}

      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">{t("dashboards.share.links")}</h3>
        {shares.data.shares.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("dashboards.share.none")}</p>
        ) : (
          <ul className="flex flex-col gap-2" data-testid="share-list">
            {shares.data.shares.map((s) => {
              const status = shareStatus(s, now);
              return (
                <li key={s.id} className="flex min-w-0 flex-col gap-1 rounded-lg border px-3 py-2 text-sm" data-testid="share-item">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="min-w-0 truncate font-medium">{s.label || t("dashboards.share.unnamed")}</span>
                    <Badge variant={status === "active" ? "muted" : "outline"}>{t(`dashboards.share.status.${status}`)}</Badge>
                  </div>
                  <p className="text-xs break-words text-muted-foreground">
                    {s.range ? t("dashboards.share.rangeRelative", { range: s.range }) : t("dashboards.share.rangeFixed", { from: formatDateTime(parseTs(s.from ?? ""), locale), to: formatDateTime(parseTs(s.to ?? ""), locale) })}
                    {" · "}
                    {t("dashboards.share.expires", { time: formatDateTime(parseTs(s.expires_at), locale) })}
                    {" · "}
                    {s.last_used_at ? t("dashboards.share.lastUsed", { time: formatDateTime(parseTs(s.last_used_at), locale) }) : t("dashboards.share.neverUsed")}
                    {s.created_by_email && ` · ${s.created_by_email}`}
                  </p>
                  {status === "active" && (
                    <RevokeButton label={s.label || t("dashboards.share.unnamed")} pending={revoke.isPending && revoke.variables === s.id} onRevoke={() => revoke.mutate(s.id)} />
                  )}
                </li>
              );
            })}
          </ul>
        )}
        <FormError error={revoke.error} />
      </section>

      {enabled && settings.data.can_edit && (
        <section className="flex flex-col gap-2 border-t pt-4">
          <p className="text-xs text-muted-foreground">{t("dashboards.share.disableHint")}</p>
          <Button variant="outline" className="w-fit" onClick={() => toggleSharing.mutate(false)} disabled={toggleSharing.isPending} data-testid="share-disable">
            {t("dashboards.share.disable")}
          </Button>
        </section>
      )}
      <FormError error={toggleSharing.error} />
    </div>
  );
}

function RevokeButton({ label, pending, onRevoke }: { label: string; pending: boolean; onRevoke: () => void }) {
  const { t } = useTranslation();
  const [confirm, setConfirm] = useState(false);
  return confirm ? (
    <div className="flex flex-wrap gap-2">
      <Button size="sm" variant="destructive" onClick={onRevoke} disabled={pending} data-testid="share-revoke-confirm">
        {t("dashboards.share.confirmRevoke")}
      </Button>
      <Button size="sm" variant="outline" onClick={() => setConfirm(false)}>
        {t("common.cancel")}
      </Button>
    </div>
  ) : (
    <Button size="sm" variant="outline" className="w-fit" onClick={() => setConfirm(true)} aria-label={t("dashboards.share.revokeNamed", { name: label })} data-testid="share-revoke">
      {t("dashboards.share.revoke")}
    </Button>
  );
}

function CreateShareForm({ dashboard, range, vars, onCreated }: { dashboard: Dashboard; range: RangeSpec; vars: VarValues | undefined; onCreated: (s: DashboardShareCreated) => void }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const uid = useId();
  const custom = isCustomRange(range);
  const [label, setLabel] = useState("");
  const [days, setDays] = useState<number>(7);
  const [mode, setMode] = useState<"relative" | "fixed">(custom ? "fixed" : "relative");
  const [relative, setRelative] = useState<string>(!custom && range.range ? range.range : "24h");
  const locked = lockedVariables(dashboard.variables, vars);
  const [openedAt] = useState(() => Date.now());
  const fixed = resolveRange(range, openedAt);
  const create = useMutation({
    mutationFn: () =>
      createDashboardShare(dashboard.id, {
        label: label.trim() || undefined,
        expires_at: expiresAt(days),
        ...(mode === "relative" ? { range: relative } : { from: new Date(fixed.from).toISOString(), to: new Date(fixed.to).toISOString() }),
        variables: locked,
      }),
    onSuccess: (s) => {
      setLabel("");
      onCreated(s);
    },
  });
  const relativeOptions = [...new Set([...PRESET_RANGES, "30d", relative])];
  const lockedText = Object.entries(locked)
    .map(([name, values]) => `${dashboard.variables.find((v) => v.name === name)?.label || name} = ${values.join(", ")}`)
    .join("; ");

  return (
    <form
      className="flex flex-col gap-3 rounded-lg border p-3"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
      data-testid="share-create"
    >
      <h3 className="text-sm font-semibold">{t("dashboards.share.create")}</h3>
      <div className="flex flex-col gap-1">
        <Label htmlFor={`${uid}-label`}>{t("dashboards.share.label")}</Label>
        <Input id={`${uid}-label`} value={label} maxLength={100} onChange={(e) => setLabel(e.target.value)} placeholder={t("dashboards.share.labelPlaceholder")} />
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${uid}-expiry`}>{t("dashboards.share.expiry")}</Label>
          <NativeSelect id={`${uid}-expiry`} value={String(days)} onChange={(e) => setDays(Number(e.target.value))}>
            {SHARE_EXPIRY_DAYS.map((d) => (
              <option key={d} value={d}>
                {t("dashboards.share.days", { count: d })}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${uid}-mode`}>{t("dashboards.share.range")}</Label>
          <NativeSelect id={`${uid}-mode`} value={mode} onChange={(e) => setMode(e.target.value as "relative" | "fixed")}>
            <option value="relative">{t("dashboards.share.modeRelative")}</option>
            <option value="fixed">{t("dashboards.share.modeFixed")}</option>
          </NativeSelect>
        </div>
      </div>
      {mode === "relative" ? (
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${uid}-relative`}>{t("dashboards.share.relativeRange")}</Label>
          <NativeSelect id={`${uid}-relative`} value={relative} onChange={(e) => setRelative(e.target.value)}>
            {relativeOptions.map((r) => (
              <option key={r} value={r}>
                {t("dashboards.share.rangeRelative", { range: r })}
              </option>
            ))}
          </NativeSelect>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t("dashboards.share.rangeFixed", { from: formatDateTime(fixed.from, locale), to: formatDateTime(fixed.to, locale) })}</p>
      )}
      <p className="text-xs break-words text-muted-foreground">{lockedText ? t("dashboards.share.lockedVariables", { variables: lockedText }) : t("dashboards.share.noVariables")}</p>
      <FormError error={create.error} />
      <Button type="submit" className="w-fit" disabled={create.isPending} data-testid="share-create-submit">
        <Link2 aria-hidden="true" />
        {t("dashboards.share.createButton")}
      </Button>
    </form>
  );
}

function CreatedLink({ share }: { share: DashboardShareCreated }) {
  const { t } = useTranslation();
  const uid = useId();
  const [copied, setCopied] = useState(false);
  const url = shareUrl(share.path);
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-primary/50 bg-primary/5 p-3" data-testid="share-created">
      <Label htmlFor={`${uid}-url`}>{t("dashboards.share.created")}</Label>
      <div className="flex min-w-0 gap-2">
        <Input id={`${uid}-url`} readOnly value={url} className="min-w-0 flex-1 font-mono text-xs" onFocus={(e) => e.currentTarget.select()} data-testid="share-url" />
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={t("dashboards.share.copy")}
          onClick={() => {
            void globalThis.navigator?.clipboard?.writeText(url).then(() => setCopied(true), () => setCopied(false));
          }}
        >
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">{t("dashboards.share.copyOnce")}</p>
    </div>
  );
}
