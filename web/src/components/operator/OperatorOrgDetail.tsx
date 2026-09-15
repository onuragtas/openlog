import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  operatorOrgQuery,
  runOperatorAction,
  startOrExtendTrial,
  startSupportSession,
  type OperatorAction,
  type OperatorOrgDetail as Detail,
} from "@/api/operator";
import { setSupportSession } from "@/api/supportSession";
import { plansQuery } from "@/api/usage";
import { PageHeader } from "@/components/AppShell";
import { DateTimeText, SettingsSection } from "@/components/settings/common";
import { PlanOverride } from "@/components/settings/UsageSettings";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { formatBytes } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { FlagCard, OperatorGuard, StateBadge } from "./OperatorConsole";
import { OperatorDeletionSection } from "./OperatorDeletion";
import { ReasonAction } from "./ReasonAction";

export function OperatorOrgDetail({ orgId }: { orgId?: string }) {
  const { t } = useTranslation();
  const params = useParams({ strict: false }) as { orgId?: string };
  const ref = orgId ?? params.orgId ?? "";
  return (
    <div className="mx-auto w-full max-w-7xl">
      <Link to="/operator" className="mb-2 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" aria-hidden="true" />
        {t("operator.back")}
      </Link>
      <OperatorGuard>
        <DetailBody orgRef={ref} />
      </OperatorGuard>
    </div>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  );
}

function DetailBody({ orgRef }: { orgRef: string }) {
  const { t } = useTranslation();
  const q = useQuery(operatorOrgQuery(orgRef));
  if (q.isPending) return <LoadingState />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  const d = q.data;
  const o = d.organization;
  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={o.name} subtitle={o.tenant_id} actions={<StateBadge state={o.state} />} />
      <Actions detail={d} />
      <div className="grid gap-4 lg:grid-cols-2">
        <SettingsSection title={t("operator.detail.overview")}>
          <dl className="grid grid-cols-1 gap-x-4 gap-y-2 text-sm sm:grid-cols-[11rem_1fr]">
            <Row label={t("operator.detail.tenant")}>
              <code className="font-mono text-xs">{o.tenant_id}</code> · <code className="font-mono text-xs">{o.id}</code>
            </Row>
            <Row label={t("operator.detail.created")}>
              <DateTimeText value={o.created_at} />
            </Row>
            <Row label={t("operator.detail.plan")}>
              {o.plan_id}
              {!o.plan_assigned && <span className="text-muted-foreground"> (default)</span>}
            </Row>
            {o.suspended_at && (
              <Row label={t("operator.states.suspended")}>
                <DateTimeText value={o.suspended_at} /> — {o.suspend_reason}
              </Row>
            )}
            {o.trial_ends_at && (
              <Row label={t("operator.detail.trialEnds")}>
                {o.trial_plan_id} · <DateTimeText value={o.trial_ends_at} />
              </Row>
            )}
            <Row label={t("operator.detail.supportAccess")}>
              {o.support_access_until ? <DateTimeText value={o.support_access_until} /> : "—"}
              {o.support_access_granted_by && <span className="text-muted-foreground"> ({o.support_access_granted_by})</span>}
            </Row>
            <Row label={t("operator.detail.lastIngest")}>
              <DateTimeText value={o.keys.last_ingest_at} relative />
            </Row>
            <Row label={t("operator.detail.keys")}>
              {t("operator.detail.keysValue", {
                license: o.keys.license_keys_active,
                revoked: o.keys.license_keys_revoked,
                api: o.keys.api_keys_active,
                scim: o.keys.scim_tokens_active,
              })}
            </Row>
            <Row label={t("operator.detail.domains")}>{o.verified_domains}</Row>
            <Row label={t("operator.detail.invitations")}>{o.pending_invitations}</Row>
          </dl>
        </SettingsSection>
        <SettingsSection title={t("operator.detail.quota")}>
          {d.quota ? (
            <ul className="flex flex-col gap-2 text-sm">
              {d.quota.metrics.map((m) => (
                <li key={m.metric} className="flex flex-col gap-1">
                  <span className="flex justify-between gap-2">
                    <span>{t(`usage.metrics.${m.metric}`)}</span>
                    <span className="tabular-nums">
                      {m.metric === "ingest_bytes" ? formatBytes(m.used) : m.used} / {m.limit > 0 ? (m.metric === "ingest_bytes" ? formatBytes(m.limit) : m.limit) : "∞"}
                    </span>
                  </span>
                  <span className="h-2 overflow-hidden rounded bg-muted" aria-hidden="true">
                    <span
                      className={`block h-full ${m.level === "exceeded" ? "bg-destructive" : m.level === "warning" ? "bg-warning" : "bg-primary"}`}
                      style={{ width: `${Math.min(100, m.percent)}%` }}
                    />
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-sm text-muted-foreground">{t("operator.detail.noQuota")}</p>
          )}
          <UsageBars detail={d} />
        </SettingsSection>
      </div>
      <SettingsSection title={t("operator.detail.planAssignment")}>
        <PlanOverride orgId={o.id} />
      </SettingsSection>
      <SettingsSection title={t("operator.detail.members")}>
        <ul className="flex flex-col divide-y text-sm">
          {o.member_list.map((m) => (
            <li key={m.user_id} className="flex flex-wrap items-center gap-2 py-1.5">
              <span className="min-w-0 break-all">{m.email}</span>
              <Badge variant="secondary">{t(`settings.roles.${m.role}`)}</Badge>
              {!m.email_verified && <Badge variant="warning">{t("operator.detail.unverified")}</Badge>}
              {m.disabled && <Badge variant="muted">{t("operator.detail.disabled")}</Badge>}
              <span className="ml-auto text-xs text-muted-foreground">
                <DateTimeText value={m.last_login_at} relative />
              </span>
            </li>
          ))}
        </ul>
      </SettingsSection>
      <div className="grid gap-4 lg:grid-cols-2">
        <SettingsSection title={t("operator.detail.sso")}>
          {o.sso_connections.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("operator.detail.noSso")}</p>
          ) : (
            <ul className="flex flex-col gap-1 text-sm">
              {o.sso_connections.map((c) => (
                <li key={c.id} className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{c.name || c.protocol.toUpperCase()}</span>
                  <Badge variant="outline">{c.protocol}</Badge>
                  {c.enabled && <Badge variant="success">{t("operator.detail.ssoEnabled")}</Badge>}
                  {c.enforce && <Badge variant="warning">{t("operator.detail.ssoEnforced")}</Badge>}
                  {c.jit_enabled && <Badge variant="muted">{t("operator.detail.ssoJit")}</Badge>}
                </li>
              ))}
            </ul>
          )}
        </SettingsSection>
        <SettingsSection title={t("operator.detail.supportSessions")}>
          {o.support_sessions.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("operator.detail.noSupportSessions")}</p>
          ) : (
            <ul className="flex flex-col gap-1 text-sm">
              {o.support_sessions.map((s) => (
                <li key={s.id}>
                  {s.operator_email} · <DateTimeText value={s.started_at} /> · {s.reason}
                </li>
              ))}
            </ul>
          )}
        </SettingsSection>
      </div>
      {o.flags.length > 0 && (
        <SettingsSection title={t("operator.detail.flags")}>
          <ul className="flex flex-col gap-2">
            {o.flags.map((f) => (
              <FlagCard key={f.id} flag={f} showOrg={false} />
            ))}
          </ul>
        </SettingsSection>
      )}
      <OperatorDeletionSection org={o} />
      <SettingsSection title={t("operator.detail.audit")}>
        <ul className="flex flex-col divide-y text-sm">
          {d.audit.map((e) => (
            <li key={e.id} className="flex flex-wrap gap-x-3 gap-y-1 py-1.5">
              <code className="font-mono text-xs">{e.action}</code>
              <span className="break-all text-muted-foreground">{e.actor_email}</span>
              <span className="ml-auto text-xs text-muted-foreground">
                <DateTimeText value={e.created_at} />
              </span>
            </li>
          ))}
        </ul>
      </SettingsSection>
    </div>
  );
}

function UsageBars({ detail }: { detail: Detail }) {
  const { t } = useTranslation();
  const days = detail.usage.days;
  if (!detail.usage.available) return <p className="text-sm text-muted-foreground">{t("operator.detail.usageUnavailable")}</p>;
  if (days.length === 0) return null;
  const max = Math.max(1, ...days.map((d) => d.ingest_bytes));
  return (
    <figure className="mt-2">
      <figcaption className="mb-1 text-xs text-muted-foreground">{t("operator.detail.usage")}</figcaption>
      <div className="flex h-24 items-end gap-0.5" role="list">
        {days.map((d) => (
          <span
            key={d.day}
            role="listitem"
            title={`${d.day}: ${formatBytes(d.ingest_bytes)}`}
            aria-label={`${d.day}: ${formatBytes(d.ingest_bytes)}`}
            className="min-w-0 flex-1 rounded-t bg-primary/70"
            style={{ height: `${Math.max(2, (d.ingest_bytes / max) * 100)}%` }}
          />
        ))}
      </div>
    </figure>
  );
}

function Actions({ detail }: { detail: Detail }) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const o = detail.organization;
  const plans = useQuery(plansQuery()).data?.plans ?? [];
  // Only plans of the catalog with trial_days can be trialled (D-106); the days default to the chosen plan's.
  const trialPlans = plans.filter((p) => p.trial_days > 0);
  const [trialPlan, setTrialPlan] = useState("");
  const [trialDays, setTrialDays] = useState(() => (detail.lifecycle.trial_ends_at && !detail.lifecycle.trial_ended_at ? "14" : ""));
  const refresh = () => void qc.invalidateQueries({ queryKey: ["operator"] });
  const act = (action: OperatorAction) => async (reason: string) => {
    await runOperatorAction(o.id, action, reason);
    refresh();
  };
  const now = useNow();
  const trialActive = !!detail.lifecycle.trial_ends_at && !detail.lifecycle.trial_ended_at;
  const supportAllowed = !!o.support_access_until && Date.parse(o.support_access_until) > now;

  return (
    <SettingsSection title={t("operator.actions.title")}>
      <div className="flex flex-wrap items-start gap-2">
        {o.state === "suspended" ? (
          <ReasonAction label={t("operator.actions.unsuspend")} onConfirm={act("unsuspend")} />
        ) : (
          <ReasonAction label={t("operator.actions.suspend")} destructive onConfirm={act("suspend")} />
        )}
        <ReasonAction
          label={trialActive ? t("operator.actions.extendTrial") : t("operator.actions.trial")}
          onConfirm={async (reason) => {
            const days = Number(trialDays);
            await startOrExtendTrial(o.id, { reason, days: Number.isFinite(days) && days > 0 ? days : undefined, plan_id: trialActive ? undefined : trialPlan || undefined });
            refresh();
          }}
        >
          {!trialActive && (
            <>
              <label htmlFor={`${id}-trial-plan`} className="text-xs text-muted-foreground">
                {t("operator.actions.trialPlan")}
              </label>
              <NativeSelect
                id={`${id}-trial-plan`}
                value={trialPlan}
                onChange={(e) => {
                  setTrialPlan(e.target.value);
                  const plan = trialPlans.find((p) => p.id === e.target.value);
                  setTrialDays(plan ? String(plan.trial_days) : "");
                }}
              >
                <option value="" />
                {trialPlans.map((p) => (
                  <option key={p.id} value={p.id}>
                    {t("operator.actions.trialPlanOption", { name: p.name || p.id, count: p.trial_days })}
                  </option>
                ))}
              </NativeSelect>
              {trialPlans.length === 0 && <p className="text-xs text-muted-foreground">{t("operator.actions.noTrialPlans")}</p>}
            </>
          )}
          <label htmlFor={`${id}-trial-days`} className="text-xs text-muted-foreground">
            {t("operator.actions.trialDays")}
          </label>
          <Input
            id={`${id}-trial-days`}
            type="number"
            min={1}
            max={365}
            value={trialDays}
            placeholder={trialActive ? "" : t("operator.actions.trialDaysDefault")}
            onChange={(e) => setTrialDays(e.target.value)}
            className="max-w-32"
          />
        </ReasonAction>
        <ReasonAction label={t("operator.actions.resetNotifications")} onConfirm={act("reset-quota-notifications")} />
        <ReasonAction label={t("operator.actions.forceLogout")} destructive onConfirm={act("force-logout")} />
        <ReasonAction label={t("operator.actions.resendVerification")} onConfirm={act("resend-verification")} />
        <ReasonAction
          label={t("operator.actions.support")}
          disabled={!supportAllowed}
          hint={t("operator.actions.supportUnavailable")}
          onConfirm={async (reason) => {
            const s = await startSupportSession(o.id, reason);
            setSupportSession({ id: s.id, org_id: s.org_id, org_name: s.org_name, expires_at: s.expires_at });
            qc.clear();
            void navigate({ to: "/hosts" });
          }}
        />
      </div>
      {!supportAllowed && <EmptyState className="py-2 text-xs">{t("operator.actions.supportUnavailable")}</EmptyState>}
    </SettingsSection>
  );
}
