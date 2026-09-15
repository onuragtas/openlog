// Remote integration configuration on the host integration panel (D-039): an inline form for the
// instance, a "collect on this host" switch, the apply state after saving and the config.yaml snippet
// as a collapsible manual alternative.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, CircleAlert, CircleX, Copy, Loader2, Send } from "lucide-react";
import { useId, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import {
  createIntegrationSetting,
  deleteIntegrationSetting,
  integrationSettingsQuery,
  PENDING_REFRESH_MS,
  updateIntegrationSetting,
  type IntegrationName,
} from "@/api/integrationSettings";
import { servicesQuery } from "@/api/queries";
import { CopyCommand } from "@/components/onboarding/CopyCommand";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { AGENT_CONFIG_PATH, agentRestart, type HostOs } from "@/lib/host-os";
import {
  applyPhase,
  CONFIG_FIELDS,
  disabledOnHost,
  draftFrom,
  ENDPOINT_PLACEHOLDER,
  endpointError,
  hostSetting,
  instanceInput,
  instanceSetting,
  type ApplyPhase,
  type ConfigDraft,
} from "@/lib/integration-settings";
import type { IntegrationStatus } from "@/lib/integrations";
import { cn } from "@/lib/utils";

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** Apply state of the host's settings; while a change is on its way the services snapshot is polled too. */
export function useApplyState(hostId: string, snapshotTime: string | null | undefined): { phase: ApplyPhase; markSaved: () => void } {
  const [savedAt, setSavedAt] = useState<number | null>(null);
  const settings = useQuery(integrationSettingsQuery(hostId));
  const phase = applyPhase(settings.data?.host, savedAt, snapshotTime);
  useQuery({ ...servicesQuery(hostId), refetchInterval: phase === "sent" || phase === "awaiting_status" ? PENDING_REFRESH_MS : false });
  return { phase, markSaved: () => setSavedAt(Date.now()) };
}

export function ApplyNotice({ phase }: { phase: ApplyPhase }) {
  const { t } = useTranslation();
  if (phase === "idle") return <p className="sr-only" aria-live="polite" />;
  const text = phase === "sent" ? t("integrations.panel.config.phase.sent") : phase === "awaiting_status" ? t("integrations.panel.config.phase.awaitingStatus") : t("integrations.panel.config.phase.disabled");
  return (
    <div
      role="status"
      data-testid="integration-apply-state"
      className={cn("flex items-start gap-2 rounded-lg border p-3 text-sm", phase === "disabled" ? "border-warning/50 bg-warning/10" : "bg-muted/50")}
    >
      {phase === "disabled" ? <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" /> : <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin" aria-hidden="true" />}
      <p className="min-w-0 break-words">{text}</p>
    </div>
  );
}

interface ConfigPanelProps {
  hostId: string;
  hostName: string;
  instance: string;
  integration: IntegrationName;
  status: IntegrationStatus;
  error?: string;
  hint?: string;
  canManage: boolean;
  onSaved: () => void;
  /** OS of the host (os.type): the manual snippet names its config.yaml path and restart command. */
  os?: HostOs;
}

/** needs_configuration / error: what went wrong, the inline form and the manual snippet. */
export function IntegrationConfigPanel({ hostId, hostName, instance, integration, status, error, hint, canManage, onSaved, os = "linux" }: ConfigPanelProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const settings = useQuery(integrationSettingsQuery(hostId));
  const existing = settings.data ? instanceSetting(settings.data.items, hostId, integration, instance) : undefined;
  const isError = status === "error";
  const Icon = isError ? CircleX : CircleAlert;

  return (
    <section
      aria-labelledby={titleId}
      data-testid="integration-config-help"
      className={cn("rounded-xl border p-4", isError ? "border-destructive/40 bg-destructive/10" : "border-warning/50 bg-warning/10")}
    >
      <h2 id={titleId} className="flex items-center gap-2 text-base font-semibold">
        <Icon className={cn("size-4 shrink-0", isError && "text-destructive-text")} aria-hidden="true" />
        {isError ? t("integrations.panel.errorTitle") : t("integrations.panel.needsConfigTitle")}
      </h2>
      {error && <p className="mt-2 font-mono text-sm break-words">{error}</p>}

      {CONFIG_FIELDS[integration].length === 0 && (
        <p className="mt-3 text-sm" data-testid="integration-no-settings">
          {t("integrations.panel.config.noSettings")}
        </p>
      )}
      {CONFIG_FIELDS[integration].length > 0 && (
        <div className="mt-4 rounded-lg border bg-card p-3 sm:p-4">
          <h3 className="text-sm font-semibold">{t("integrations.panel.config.title")}</h3>
          <p className="mt-1 text-xs text-muted-foreground">{t("integrations.panel.config.intro", { host: hostName })}</p>
          {settings.isPending ? (
            <Loader2 className="mt-3 size-4 animate-spin" aria-hidden="true" />
          ) : (
            <ConfigForm
              // Remount after a save so that the password field is empty again and the stored values show.
              key={existing?.updated_at ?? "new"}
              hostId={hostId}
              instance={instance}
              integration={integration}
              existing={existing}
              canManage={canManage}
              onSaved={onSaved}
            />
          )}
        </div>
      )}

      {hint ? <ManualSnippet hint={hint} hostName={hostName} os={os} /> : <p className="mt-3 text-sm">{t("integrations.panel.noHint", { id: integration })}</p>}
    </section>
  );
}

function ConfigForm({
  hostId,
  instance,
  integration,
  existing,
  canManage,
  onSaved,
}: {
  hostId: string;
  instance: string;
  integration: IntegrationName;
  existing: ReturnType<typeof instanceSetting>;
  canManage: boolean;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const ids = { endpoint: useId(), endpointHelp: useId(), username: useId(), password: useId(), passwordHelp: useId(), database: useId(), error: useId() };
  const qc = useQueryClient();
  const [draft, setDraft] = useState<ConfigDraft>(() => draftFrom(existing));
  const [touched, setTouched] = useState(false);
  const fields = CONFIG_FIELDS[integration];
  const epError = endpointError(integration, draft.endpoint);
  const save = useMutation({
    mutationFn: () => {
      const body = instanceInput(integration, hostId, instance, draft, existing);
      return existing ? updateIntegrationSetting(existing.id, body) : createIntegrationSetting(body);
    },
    onSuccess: async () => {
      onSaved();
      await qc.invalidateQueries({ queryKey: ["integration-settings", hostId] });
    },
  });
  const set = (p: Partial<ConfigDraft>) => {
    setDraft((d) => ({ ...d, ...p }));
    save.reset();
  };
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (epError || !canManage) return;
    save.mutate();
  };
  const disabled = !canManage || save.isPending;
  const saveError = save.error
    ? save.error instanceof ApiError && save.error.code === "failed_precondition" && /SECRETS_KEY/.test(save.error.message)
      ? t("integrations.panel.config.noKey")
      : errorText(save.error)
    : null;

  return (
    <form className="mt-3 flex flex-col gap-3" onSubmit={onSubmit} noValidate aria-label={t("integrations.panel.config.title")}>
      {!canManage && <p className="text-xs text-muted-foreground">{t("integrations.panel.config.readOnly")}</p>}
      {/* One column on phones, two from 640px. */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {fields.includes("endpoint") && (
          <div className="flex min-w-0 flex-col gap-1.5 sm:col-span-2">
            <label htmlFor={ids.endpoint} className="text-sm font-medium">
              {t("integrations.panel.config.endpoint")}
            </label>
            <Input
              id={ids.endpoint}
              value={draft.endpoint}
              placeholder={ENDPOINT_PLACEHOLDER[integration]}
              disabled={disabled}
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              aria-invalid={touched && !!epError}
              aria-describedby={ids.endpointHelp}
              onChange={(e) => set({ endpoint: e.target.value })}
              onBlur={() => setTouched(true)}
            />
            <p id={ids.endpointHelp} className={cn("text-xs", touched && epError ? "text-destructive-text" : "text-muted-foreground")}>
              {touched && epError
                ? t(`integrations.panel.config.errors.${epError}`)
                : integration === "nginx"
                  ? t("integrations.panel.config.endpointHelpNginx")
                  : integration === "mssql"
                    ? t("integrations.panel.config.endpointHelpMssql")
                    : t("integrations.panel.config.endpointHelp")}
            </p>
          </div>
        )}
        {fields.includes("username") && (
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor={ids.username} className="text-sm font-medium">
              {t("integrations.panel.config.username")}
            </label>
            <Input id={ids.username} value={draft.username} disabled={disabled} autoComplete="off" spellCheck={false} onChange={(e) => set({ username: e.target.value })} />
          </div>
        )}
        {fields.includes("password") && (
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor={ids.password} className="text-sm font-medium">
              {t("integrations.panel.config.password")}
            </label>
            <Input
              id={ids.password}
              type="password"
              value={draft.password}
              disabled={disabled || draft.clearPassword}
              autoComplete="new-password"
              aria-describedby={existing?.password_set ? ids.passwordHelp : undefined}
              placeholder={existing?.password_set ? "••••••••" : ""}
              onChange={(e) => set({ password: e.target.value })}
            />
            {existing?.password_set && (
              <>
                <p id={ids.passwordHelp} className="text-xs text-muted-foreground">
                  {t("integrations.panel.config.passwordSet")}
                </p>
                <label className="flex min-h-10 items-center gap-2 text-xs sm:min-h-0">
                  <input type="checkbox" className="size-4 accent-primary" disabled={disabled} checked={draft.clearPassword} onChange={(e) => set({ clearPassword: e.target.checked, password: "" })} />
                  {t("integrations.panel.config.clearPassword")}
                </label>
              </>
            )}
          </div>
        )}
        {fields.includes("database") && (
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor={ids.database} className="text-sm font-medium">
              {t("integrations.panel.config.database")}
            </label>
            <Input
              id={ids.database}
              value={draft.database}
              placeholder={t("integrations.panel.config.databasePlaceholder")}
              disabled={disabled}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => set({ database: e.target.value })}
            />
          </div>
        )}
      </div>
      <label className="flex min-h-10 w-fit items-center gap-2 text-sm">
        <input type="checkbox" className="size-4 accent-primary" disabled={disabled} checked={draft.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
        {t("integrations.panel.config.enabled")}
      </label>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <Button type="submit" className="min-h-10 w-full sm:w-auto" disabled={disabled}>
          {save.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Send aria-hidden="true" />}
          {save.isPending ? t("integrations.panel.config.saving") : t("integrations.panel.config.save")}
        </Button>
        <p className="text-sm" aria-live="polite">
          {save.isSuccess && t("integrations.panel.config.saved")}
        </p>
      </div>
      {saveError && (
        <p id={ids.error} role="alert" className="text-sm break-words text-destructive-text">
          {saveError}
        </p>
      )}
    </form>
  );
}

function ManualSnippet({ hint, hostName, os }: { hint: string; hostName: string; os: HostOs }) {
  const { t } = useTranslation();
  const [copy, setCopy] = useState<"idle" | "copied" | "failed">("idle");
  const restart = agentRestart(os);
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(hint);
      setCopy("copied");
    } catch {
      setCopy("failed");
    }
  };
  return (
    <details className="group mt-3" data-testid="integration-manual-config">
      <summary className="flex min-h-10 cursor-pointer list-none items-center gap-1 text-sm font-medium [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-4 shrink-0 transition-transform group-open:rotate-90" aria-hidden="true" />
        {t("integrations.panel.manualTitle")}
      </summary>
      <p className="mt-2 text-sm break-words" data-os={os}>
        {t("integrations.panel.manualIntro",{ host: hostName, path: AGENT_CONFIG_PATH[os] })}
      </p>
      <div className="mt-2 overflow-hidden rounded-md border bg-card">
        <div className="flex items-center justify-between gap-2 border-b px-3 py-1.5">
          <span className="text-xs text-muted-foreground">{t("integrations.panel.hintLabel")}</span>
          <Button variant="ghost" size="sm" className="min-h-10" onClick={() => void onCopy()}>
            <Copy aria-hidden="true" />
            {copy === "copied" ? t("integrations.panel.copied") : t("integrations.panel.copy")}
          </Button>
        </div>
        <pre className="overflow-x-auto p-3 text-xs leading-relaxed" aria-label={t("integrations.panel.hintLabel")}>
          <code>{hint}</code>
        </pre>
      </div>
      <p className="sr-only" aria-live="polite">
        {copy === "copied" ? t("integrations.panel.copied") : copy === "failed" ? t("integrations.panel.copyFailed") : ""}
      </p>
      {copy === "failed" && <p className="mt-1 text-xs text-destructive-text">{t("integrations.panel.copyFailed")}</p>}
      <div className="mt-2 text-xs">
        <CopyCommand code={restart.code} label={t("integrations.panel.restartLabel")} lang={restart.lang} testId="integration-restart" />
      </div>
    </details>
  );
}

/** "Collect <name> metrics on this host": a host-scoped setting without a match. */
export function HostIntegrationToggle({
  hostId,
  hostName,
  integration,
  name,
  canManage,
  onSaved,
}: {
  hostId: string;
  hostName: string;
  integration: IntegrationName;
  name: string;
  canManage: boolean;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const helpId = useId();
  const qc = useQueryClient();
  const settings = useQuery(integrationSettingsQuery(hostId));
  const items = settings.data?.items ?? [];
  const on = !disabledOnHost(items, hostId, integration);
  const toggle = useMutation({
    mutationFn: async (enable: boolean) => {
      const host = hostSetting(items, hostId, integration);
      if (host) {
        const plain = !host.endpoint && !host.username && !host.password_set && !host.database && host.databases.length === 0;
        const allHostsOff = items.some((s) => s.host_id === null && s.integration === integration && !s.enabled && !s.match.instance && !s.match.port && !s.match.container && !s.match.endpoint);
        if (enable && plain && !allHostsOff) return deleteIntegrationSetting(host.id);
        const { endpoint, username, database, databases } = host;
        return updateIntegrationSetting(host.id, { host_id: hostId, integration, enabled: enable, endpoint, username, database, databases, password: null });
      }
      return createIntegrationSetting({ host_id: hostId, integration, enabled: enable });
    },
    onSuccess: async () => {
      onSaved();
      await qc.invalidateQueries({ queryKey: ["integration-settings", hostId] });
    },
  });
  const busy = settings.isPending || toggle.isPending;

  return (
    <div className="flex flex-col gap-1 rounded-lg border p-3" data-testid="integration-host-toggle">
      <label htmlFor={id} className={cn("flex min-h-10 w-fit items-center gap-3 text-sm font-medium", canManage && "cursor-pointer")}>
        <button
          id={id}
          type="button"
          role="switch"
          aria-checked={on}
          aria-describedby={helpId}
          disabled={!canManage || busy}
          onClick={() => toggle.mutate(!on)}
          className={cn(
            "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 pointer-coarse:h-6 pointer-coarse:w-11",
            on ? "bg-primary" : "bg-input",
          )}
        >
          <span
            className={cn(
              "pointer-events-none block size-4 rounded-full bg-background shadow-sm transition-transform pointer-coarse:size-5",
              on ? "translate-x-4 pointer-coarse:translate-x-5" : "translate-x-0",
            )}
          />
        </button>
        {t("integrations.panel.config.hostToggle", { name })}
      </label>
      <p id={helpId} className="text-xs text-muted-foreground">
        {canManage ? t("integrations.panel.config.hostToggleHelp", { name, host: hostName }) : t("integrations.panel.config.readOnly")}
      </p>
      {toggle.error && (
        <p role="alert" className="text-xs break-words text-destructive-text">
          {t("integrations.panel.config.hostToggleFailed", { error: errorText(toggle.error) })}
        </p>
      )}
    </div>
  );
}
