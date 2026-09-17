// Browser keys of the RUM SDK (docs/contracts/rum.md §3). A browser key is public by construction — it ships
// inside a web page — so this screen is about capability, not secrecy: which application a page may write
// under, which origins may use the key, how much it may send, and revoking one that is being misused.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe, Loader2, Plus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { browserKeysQuery, createBrowserKey, revokeBrowserKey, updateBrowserKey, type BrowserKey, type BrowserKeyInput } from "@/api/browserKeys";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { usePermissions } from "@/lib/org-writable";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SecretReveal } from "./SecretReveal";

const DEFAULT_RATE_LIMIT = 6000;

interface Draft {
  name: string;
  serviceName: string;
  environment: string;
  origins: string;
  rateLimit: string;
  sampleRate: string;
}

const newDraft = (): Draft => ({ name: "", serviceName: "", environment: "", origins: "", rateLimit: String(DEFAULT_RATE_LIMIT), sampleRate: "1" });

const draftOf = (k: BrowserKey): Draft => ({
  name: k.name,
  serviceName: k.service_name,
  environment: k.environment,
  origins: k.origins.join(", "),
  rateLimit: String(k.rate_limit_per_minute),
  sampleRate: String(k.sample_rate),
});

/** Origins are typed as a list. The server refuses an empty one: a blank field must not be the unsafe setting (rum.md §3.2). */
const parseOrigins = (s: string): string[] => s.split(/[\s,]+/).filter((o) => o !== "");

const toInput = (d: Draft): BrowserKeyInput => ({
  name: d.name.trim(),
  service_name: d.serviceName.trim(),
  environment: d.environment.trim() || undefined,
  origins: parseOrigins(d.origins),
  rate_limit_per_minute: Number(d.rateLimit) || DEFAULT_RATE_LIMIT,
  sample_rate: Number(d.sampleRate),
});

const complete = (d: Draft) => d.name.trim() !== "" && d.serviceName.trim() !== "" && parseOrigins(d.origins).length > 0;

function KeyFields({ id, draft, onChange, showName }: { id: string; draft: Draft; onChange: (d: Draft) => void; showName: boolean }) {
  const { t } = useTranslation();
  const set = (patch: Partial<Draft>) => onChange({ ...draft, ...patch });
  return (
    <div className="flex flex-wrap items-end gap-2">
      {showName && (
        <div className="flex min-w-48 flex-1 flex-col gap-1.5">
          <Label htmlFor={`${id}-name`}>{t("settings.browserKeys.name")}</Label>
          <Input id={`${id}-name`} value={draft.name} maxLength={200} placeholder={t("settings.browserKeys.namePlaceholder")} onChange={(e) => set({ name: e.target.value })} />
        </div>
      )}
      <div className="flex min-w-48 flex-1 flex-col gap-1.5">
        <Label htmlFor={`${id}-service`}>{t("settings.browserKeys.application")}</Label>
        <Input
          id={`${id}-service`}
          value={draft.serviceName}
          maxLength={200}
          placeholder={t("settings.browserKeys.applicationPlaceholder")}
          onChange={(e) => set({ serviceName: e.target.value })}
        />
      </div>
      <div className="flex min-w-36 flex-col gap-1.5">
        <Label htmlFor={`${id}-env`}>{t("settings.browserKeys.environment")}</Label>
        <Input id={`${id}-env`} value={draft.environment} maxLength={200} placeholder={t("settings.browserKeys.environmentPlaceholder")} onChange={(e) => set({ environment: e.target.value })} />
      </div>
      <div className="flex min-w-60 flex-[2] flex-col gap-1.5">
        <Label htmlFor={`${id}-origins`}>{t("settings.browserKeys.origins")}</Label>
        <Input id={`${id}-origins`} value={draft.origins} placeholder={t("settings.browserKeys.originsPlaceholder")} onChange={(e) => set({ origins: e.target.value })} />
      </div>
      <div className="flex w-28 flex-col gap-1.5">
        <Label htmlFor={`${id}-rate`}>{t("settings.browserKeys.rateLimit")}</Label>
        <Input id={`${id}-rate`} type="number" min={1} step={100} value={draft.rateLimit} onChange={(e) => set({ rateLimit: e.target.value })} />
      </div>
      <div className="flex w-28 flex-col gap-1.5">
        <Label htmlFor={`${id}-sample`}>{t("settings.browserKeys.sampleRate")}</Label>
        <Input id={`${id}-sample`} type="number" min={0.01} max={1} step={0.05} value={draft.sampleRate} onChange={(e) => set({ sampleRate: e.target.value })} />
      </div>
    </div>
  );
}

export function BrowserKeysSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const perms = usePermissions();
  const canManage = perms.writable && perms.can("browser_keys.manage");
  const keys = useQuery(browserKeysQuery());
  const [draft, setDraft] = useState<Draft>(newDraft);
  const [editing, setEditing] = useState<{ id: string; draft: Draft } | null>(null);
  const [created, setCreated] = useState<{ name: string; key: string } | null>(null);

  const invalidate = () => void qc.invalidateQueries({ queryKey: browserKeysQuery().queryKey });
  const create = useMutation({
    mutationFn: (d: Draft) => createBrowserKey(toInput(d)),
    onSuccess: (res) => {
      setCreated({ name: res.browser_key.name, key: res.key });
      setDraft(newDraft());
      invalidate();
    },
  });
  const update = useMutation({
    mutationFn: (v: { id: string; draft: Draft }) => updateBrowserKey(v.id, toInput(v.draft)),
    onSuccess: () => {
      setEditing(null);
      invalidate();
    },
  });
  const revoke = useMutation({ mutationFn: (keyId: string) => revokeBrowserKey(keyId), onSettled: invalidate });

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("settings.browserKeys.title")} description={t("settings.browserKeys.description")}>
        {canManage && (
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (complete(draft)) create.mutate(draft);
            }}
          >
            <KeyFields id={`${id}-new`} draft={draft} onChange={setDraft} showName />
            <div className="flex items-center gap-3">
              <Button type="submit" disabled={create.isPending || !complete(draft)}>
                {create.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Plus aria-hidden="true" />}
                {t("settings.browserKeys.create")}
              </Button>
              <p className="text-xs text-muted-foreground">{t("settings.browserKeys.originsHelp")}</p>
            </div>
          </form>
        )}
        <FormError error={create.error ?? update.error ?? revoke.error} />
        {created && <SecretReveal label={t("settings.browserKeys.created", { name: created.name })} secret={created.key} note={t("settings.browserKeys.usage")} onDone={() => setCreated(null)} />}
      </SettingsSection>

      <div className="rounded-xl border bg-card">
        {keys.isPending ? (
          <LoadingState />
        ) : keys.isError ? (
          <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
        ) : keys.data.length === 0 ? (
          <EmptyState icon={<Globe className="size-5" aria-hidden="true" />}>{t("settings.browserKeys.empty")}</EmptyState>
        ) : (
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("settings.columns.name")}</TableHead>
                <TableHead>{t("settings.columns.key")}</TableHead>
                <TableHead>{t("settings.browserKeys.application")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("settings.browserKeys.origins")}</TableHead>
                <TableHead className="hidden md:table-cell text-right">{t("settings.browserKeys.sampleRate")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.columns.lastUsed")}</TableHead>
                <TableHead>{t("settings.columns.status")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("settings.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.data.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell label={t("settings.columns.key")}>
                    <code className="font-mono text-xs">{k.prefix}</code>
                    <span aria-hidden="true">…</span>
                  </TableCell>
                  <TableCell label={t("settings.browserKeys.application")}>
                    {k.service_name}
                    {k.environment !== "" && <span className="text-muted-foreground"> · {k.environment}</span>}
                  </TableCell>
                  <TableCell label={t("settings.browserKeys.origins")} className="hidden break-all lg:table-cell">
                    {k.origins.join(", ")}
                  </TableCell>
                  <TableCell label={t("settings.browserKeys.sampleRate")} className="hidden md:table-cell text-right tabular-nums">
                    {Math.round(k.sample_rate * 100)}%
                  </TableCell>
                  <TableCell label={t("settings.columns.lastUsed")} className="hidden md:table-cell">
                    <DateTimeText value={k.last_used_at} relative />
                  </TableCell>
                  <TableCell className="max-md:w-auto">
                    {k.revoked_at ? <Badge variant="muted">{t("settings.revoked")}</Badge> : <Badge variant="success">{t("settings.active")}</Badge>}
                  </TableCell>
                  <TableCell className="text-right">
                    {canManage && !k.revoked_at && (
                      <div className="flex justify-end gap-2">
                        <Button variant="ghost" size="sm" onClick={() => setEditing({ id: k.id, draft: draftOf(k) })}>
                          {t("settings.browserKeys.edit")}
                        </Button>
                        <ConfirmButton
                          label={t("settings.revoke")}
                          confirmLabel={t("settings.confirmRevoke")}
                          pending={revoke.isPending && revoke.variables === k.id}
                          onConfirm={() => revoke.mutate(k.id)}
                        />
                      </div>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>

      {editing && (
        <SettingsSection title={t("settings.browserKeys.editTitle")} description={t("settings.browserKeys.editDescription")}>
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (complete(editing.draft)) update.mutate(editing);
            }}
          >
            <KeyFields id={`${id}-edit`} draft={editing.draft} onChange={(d) => setEditing({ ...editing, draft: d })} showName={false} />
            <div className="flex gap-2">
              <Button type="submit" disabled={update.isPending || !complete(editing.draft)}>
                {update.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
                {t("settings.browserKeys.save")}
              </Button>
              <Button type="button" variant="ghost" onClick={() => setEditing(null)}>
                {t("settings.browserKeys.cancel")}
              </Button>
            </div>
          </form>
        </SettingsSection>
      )}
    </div>
  );
}
