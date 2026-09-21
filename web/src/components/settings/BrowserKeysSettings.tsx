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
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { usePermissions } from "@/lib/org-writable";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SecretReveal } from "./SecretReveal";

const DEFAULT_RATE_LIMIT = 6000;

// The range the server enforces (internal/auth/browserkeys.go): the form mirrors it so a value it accepts is
// a value the API accepts, and so the browser's own number validation never refuses one that is legal.
const MIN_RATE_LIMIT = 60;
const MAX_RATE_LIMIT = 10_000_000;

/** Which allowlist bounds the key. A mobile app has no origin, so it declares its own id (rum.md §3.6). */
type Kind = BrowserKey["kind"];

interface Draft {
  name: string;
  serviceName: string;
  environment: string;
  kind: Kind;
  origins: string;
  appIds: string;
  rateLimit: string;
  sampleRate: string;
}

const newDraft = (): Draft => ({
  name: "",
  serviceName: "",
  environment: "",
  kind: "browser",
  origins: "",
  appIds: "",
  rateLimit: String(DEFAULT_RATE_LIMIT),
  sampleRate: "1",
});

const draftOf = (k: BrowserKey): Draft => ({
  name: k.name,
  serviceName: k.service_name,
  environment: k.environment,
  kind: k.kind,
  origins: k.origins.join(", "),
  appIds: k.app_ids.join(", "),
  rateLimit: String(k.rate_limit_per_minute),
  sampleRate: String(k.sample_rate),
});

/** Both allowlists are typed as a list. The server refuses an empty one: a blank field must not be the unsafe setting (rum.md §3.2). */
const parseList = (s: string): string[] => s.split(/[\s,]+/).filter((o) => o !== "");

/** The list that bounds this draft: origins for a browser key, application ids for a mobile one. */
const scopeOf = (d: Draft): string[] => parseList(d.kind === "mobile" ? d.appIds : d.origins);

const toInput = (d: Draft): BrowserKeyInput => ({
  name: d.name.trim(),
  service_name: d.serviceName.trim(),
  environment: d.environment.trim() || undefined,
  kind: d.kind,
  // Exactly the allowlist belonging to the kind. Sending both is refused by the server, so the form never
  // offers it: a key whose scope depends on which check ran first is not a scope at all.
  ...(d.kind === "mobile" ? { app_ids: scopeOf(d) } : { origins: scopeOf(d) }),
  rate_limit_per_minute: Number(d.rateLimit) || DEFAULT_RATE_LIMIT,
  sample_rate: Number(d.sampleRate),
});

/** Events/min: a whole number in the server's range. */
const rateLimitError = (d: Draft): boolean => {
  const v = Number(d.rateLimit.trim());
  return !Number.isInteger(v) || v < MIN_RATE_LIMIT || v > MAX_RATE_LIMIT;
};

/** Sampling: a fraction above 0 and at most 1 (1 = keep every event). */
const sampleRateError = (d: Draft): boolean => {
  const v = Number(d.sampleRate.trim());
  return !Number.isFinite(v) || v <= 0 || v > 1;
};

const complete = (d: Draft) =>
  d.name.trim() !== "" && d.serviceName.trim() !== "" && scopeOf(d).length > 0 && !rateLimitError(d) && !sampleRateError(d);

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
      <div className="flex w-40 flex-col gap-1.5">
        <Label htmlFor={`${id}-kind`}>{t("settings.browserKeys.kind")}</Label>
        <NativeSelect id={`${id}-kind`} value={draft.kind} onChange={(e) => set({ kind: e.target.value as Kind })}>
          <option value="browser">{t("settings.browserKeys.kindBrowser")}</option>
          <option value="mobile">{t("settings.browserKeys.kindMobile")}</option>
        </NativeSelect>
      </div>
      {draft.kind === "mobile" ? (
        <div className="flex min-w-60 flex-[2] flex-col gap-1.5">
          <Label htmlFor={`${id}-app-ids`}>{t("settings.browserKeys.appIds")}</Label>
          <Input id={`${id}-app-ids`} value={draft.appIds} placeholder={t("settings.browserKeys.appIdsPlaceholder")} onChange={(e) => set({ appIds: e.target.value })} />
        </div>
      ) : (
        <div className="flex min-w-60 flex-[2] flex-col gap-1.5">
          <Label htmlFor={`${id}-origins`}>{t("settings.browserKeys.origins")}</Label>
          <Input id={`${id}-origins`} value={draft.origins} placeholder={t("settings.browserKeys.originsPlaceholder")} onChange={(e) => set({ origins: e.target.value })} />
        </div>
      )}
      <div className="flex w-32 flex-col gap-1.5">
        <Label htmlFor={`${id}-rate`}>{t("settings.browserKeys.rateLimit")}</Label>
        <Input
          id={`${id}-rate`}
          type="number"
          inputMode="numeric"
          min={MIN_RATE_LIMIT}
          max={MAX_RATE_LIMIT}
          step={1}
          aria-invalid={rateLimitError(draft)}
          value={draft.rateLimit}
          onChange={(e) => set({ rateLimit: e.target.value })}
        />
        {rateLimitError(draft) && <p className="text-xs text-destructive">{t("settings.browserKeys.rateLimitHelp", { min: MIN_RATE_LIMIT, max: MAX_RATE_LIMIT })}</p>}
      </div>
      <div className="flex w-28 flex-col gap-1.5">
        <Label htmlFor={`${id}-sample`}>{t("settings.browserKeys.sampleRate")}</Label>
        <Input
          id={`${id}-sample`}
          type="number"
          min={0}
          max={1}
          step="any"
          aria-invalid={sampleRateError(draft)}
          value={draft.sampleRate}
          onChange={(e) => set({ sampleRate: e.target.value })}
        />
        {sampleRateError(draft) && <p className="text-xs text-destructive">{t("settings.browserKeys.sampleRateHelp")}</p>}
      </div>
    </div>
  );
}

/**
 * The key in full, with a copy button.
 *
 * Every other credential in these settings shows a prefix and is revealed once, and that is right for them.
 * A browser key is the exception on purpose: it ships inside the page, so everyone who can open the site
 * already has it (rum.md §3.5) and hiding it here only forced a rotation of something the internet knew.
 * Keys created before the value was stored have none, and those still show their prefix.
 */
function KeyValue({ value }: { value: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex flex-wrap items-center gap-2">
      <code className="font-mono text-xs break-all">{value}</code>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={() => {
          navigator.clipboard
            .writeText(value)
            .then(() => setCopied(true))
            .catch(() => undefined);
        }}
      >
        {copied ? t("settings.copied") : t("common.copy")}
      </Button>
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
              <p className="text-xs text-muted-foreground">
                {draft.kind === "mobile" ? t("settings.browserKeys.appIdsHelp") : t("settings.browserKeys.originsHelp")}
              </p>
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
                <TableHead className="hidden lg:table-cell">{t("settings.browserKeys.scope")}</TableHead>
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
                    {k.key !== "" ? (
                      <KeyValue value={k.key} />
                    ) : (
                      <>
                        <code className="font-mono text-xs">{k.prefix}</code>
                        <span aria-hidden="true">…</span>
                      </>
                    )}
                  </TableCell>
                  <TableCell label={t("settings.browserKeys.application")}>
                    {k.service_name}
                    {k.environment !== "" && <span className="text-muted-foreground"> · {k.environment}</span>}
                  </TableCell>
                  <TableCell label={t("settings.browserKeys.scope")} className="hidden break-all lg:table-cell">
                    {k.kind === "mobile" ? k.app_ids.join(", ") : k.origins.join(", ")}
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
