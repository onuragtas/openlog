import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, XCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { can } from "@/api/roles";
import { ssoStateQuery } from "@/api/sso";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { SsoConnectionForm } from "./SsoConnectionForm";
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

/** Settings → Single sign-on: domains, OIDC/SAML connection, role mappings, enforcement and SCIM (admin+). */
export function SsoSettings() {
  const { t } = useTranslation();
  const me = useMe().data;
  const role = me?.role ?? null;
  const allowed = me?.auth === "session" && can(role, "org.update");
  const state = useQuery({ ...ssoStateQuery(), enabled: allowed });
  // Set by the server when a test sign-in returns (/settings/sso?sso_test=ok|failed).
  const testResult = typeof window !== "undefined" ? new URLSearchParams(window.location.search).get("sso_test") : null;

  if (me && me.auth !== "session") return <EmptyState>{t("settings.apiKeyAuth")}</EmptyState>;
  if (me && !allowed) return <EmptyState>{t("sso.adminOnly")}</EmptyState>;
  if (state.isPending) return <LoadingState />;
  if (state.isError) return <ErrorState error={state.error} onRetry={() => void state.refetch()} />;
  const s = state.data;

  return (
    <div className="flex flex-col gap-4">
      {testResult === "ok" && <Notice tone="success">{t("sso.testOk")}</Notice>}
      {testResult === "failed" && <Notice tone="error">{t("sso.testFailed")}</Notice>}
      {!s.available && <Notice tone="warning">{t("sso.unavailable")}</Notice>}
      {!s.secrets_encrypted && <Notice tone="warning">{t("sso.secretsUnencrypted")}</Notice>}
      <SsoDomains state={s} />
      <SsoConnectionForm state={s} />
      {s.connection && <SsoRoleMappings />}
      {s.connection && <SsoEnforcement state={s} />}
      <SsoScimTokens enabled={s.scim_enabled} />
    </div>
  );
}
