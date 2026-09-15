import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, XCircle } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { can } from "@/api/roles";
import { SSO_TEST_CONNECTION_KEY, ssoConnectionsQuery } from "@/api/sso";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { WriteGuard } from "@/components/ReadOnly";
import { SsoConnectionForm } from "./SsoConnectionForm";
import { SsoConnections } from "./SsoConnections";
import { SsoDomains } from "./SsoDomains";
import { SsoEnforcement } from "./SsoEnforcement";
import { SsoRoleMappings } from "./SsoRoleMappings";
import { SsoScimTokens } from "./SsoScimTokens";

function Notice({ tone, children }: { tone: "success" | "warning" | "error"; children: React.ReactNode }) {
  const Icon = tone === "success" ? CheckCircle2 : tone === "error" ? XCircle : AlertTriangle;
  const cls = tone === "success" ? "border-success/50 bg-success/10" : tone === "error" ? "border-destructive/50 bg-destructive/10" : "border-warning/60 bg-warning/10";
  return (
    <p role={tone === "success" ? "status" : "alert"} className={`flex items-start gap-2 rounded-lg border p-3 text-sm ${cls}`}>
      <Icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span>{children}</span>
    </p>
  );
}

/** The wizard being shown: `id` null = a new connection; `key` changes when another connection is opened. */
interface FormTarget {
  key: number;
  id: string | null;
}

const testResultParam = () => (typeof window !== "undefined" ? new URLSearchParams(window.location.search).get("sso_test") : null);

/** After a test sign-in returns, reopen the tested connection. */
function initialForm(): FormTarget | null {
  if (!testResultParam()) return null;
  try {
    const id = sessionStorage.getItem(SSO_TEST_CONNECTION_KEY);
    return id ? { key: 0, id } : null;
  } catch {
    return null;
  }
}

/** Settings → Single sign-on: domains, OIDC/SAML connections, role mappings, enforcement and SCIM (admin+). */
export function SsoSettings() {
  const { t } = useTranslation();
  const me = useMe().data;
  const role = me?.role ?? null;
  const allowed = me?.auth === "session" && can(role, "org.update");
  const state = useQuery({ ...ssoConnectionsQuery(), enabled: allowed });
  const [form, setForm] = useState<FormTarget | null>(initialForm);
  // Set by the server when a test sign-in returns (/settings/sso?sso_test=ok|failed); read once, the URL may change later.
  const [testResult] = useState(testResultParam);

  if (me && me.auth !== "session") return <EmptyState>{t("settings.apiKeyAuth")}</EmptyState>;
  if (me && !allowed) return <EmptyState>{t("sso.adminOnly")}</EmptyState>;
  if (state.isPending) return <LoadingState />;
  if (state.isError) return <ErrorState error={state.error} onRetry={() => void state.refetch()} />;
  const s = state.data;
  const hasConnections = s.connections.length > 0;
  const open = form && (form.id === null || s.connections.some((c) => c.id === form.id)) ? form : null;
  // Without connections the wizard is shown directly (key -1 stays when the first connection is saved).
  const target = open ?? (hasConnections ? null : { key: -1, id: null });

  // Read-only organization (suspended, support view): the settings stay visible, every control is disabled.
  return (
    <WriteGuard block>
    <div className="flex flex-col gap-4">
      {testResult === "ok" && <Notice tone="success">{t("sso.testOk")}</Notice>}
      {testResult === "failed" && <Notice tone="error">{t("sso.testFailed")}</Notice>}
      {!s.available && <Notice tone="warning">{t("sso.unavailable")}</Notice>}
      {!s.secrets_encrypted && <Notice tone="warning">{t("sso.secretsUnencrypted")}</Notice>}
      <SsoDomains state={s} />
      {hasConnections && (
        <SsoConnections
          state={s}
          editingId={target?.id ?? null}
          onEdit={(id) => setForm((f) => ({ key: (f?.key ?? 0) + 1, id }))}
          onAdd={() => setForm((f) => ({ key: (f?.key ?? 0) + 1, id: null }))}
          onDeleted={(id) => setForm((f) => (f?.id === id ? null : f))}
        />
      )}
      {target && (
        <SsoConnectionForm
          key={target.key}
          state={s}
          connectionId={target.id}
          onSaved={(c) => setForm({ key: target.key, id: c.id })}
          onClose={hasConnections ? () => setForm(null) : undefined}
        />
      )}
      {hasConnections && <SsoRoleMappings state={s} />}
      {hasConnections && <SsoEnforcement state={s} preferredId={target?.id ?? null} />}
      <SsoScimTokens enabled={s.scim_enabled} />
    </div>
    </WriteGuard>
  );
}
