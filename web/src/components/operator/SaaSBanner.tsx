import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { Ban, Clock, LifeBuoy } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { endSupportSession, orgSaaSStateQuery, recordSupportView } from "@/api/operator";
import { getSupportSession, setSupportSession } from "@/api/supportSession";
import { Button } from "@/components/ui/button";
import { formatDateTime } from "@/lib/format";
import { useSupportSessionId } from "./useSupportSession";

const bar = "flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-2 text-sm";

/** Support view, suspension and trial banners (GET /api/v1/orgs/current/saas). */
export function SaaSBanner() {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const me = useMe().data;
  const qc = useQueryClient();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const supportId = useSupportSessionId();
  const support = supportId ? getSupportSession() : null;
  const state = useQuery({ ...orgSaaSStateQuery(), enabled: !!me?.organization }).data;

  // Every page an operator opens in a support view is recorded in the organization's audit log.
  useEffect(() => {
    if (supportId) void recordSupportView(supportId, pathname).catch(() => undefined);
  }, [supportId, pathname]);

  const exit = () => {
    const id = supportId;
    setSupportSession(null);
    qc.clear();
    if (id) void endSupportSession(id).catch(() => undefined);
    void navigate({ to: "/operator" });
  };

  return (
    <>
      {support && (
        <div role="status" className={`${bar} border-destructive bg-destructive/15`} data-testid="support-banner">
          <LifeBuoy className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1 break-words">
            {t("saas.support.banner", { org: support.org_name, date: formatDateTime(Date.parse(support.expires_at), locale) })}
          </span>
          <Button type="button" size="sm" variant="destructive" onClick={exit}>
            {t("saas.support.exit")}
          </Button>
        </div>
      )}
      {state?.suspended && (
        <div role="alert" className={`${bar} border-destructive/60 bg-destructive/10`}>
          <Ban className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1 break-words">{t("saas.suspended")}</span>
        </div>
      )}
      {state?.trial && !state.suspended && (
        <div role="status" className={`${bar} border-warning/60 bg-warning/10`}>
          <Clock className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1 break-words">
            {t("saas.trial", { plan: state.trial.plan_name, date: formatDateTime(Date.parse(state.trial.ends_at), locale) })}
          </span>
          <Link to="/settings/usage" className="font-medium underline underline-offset-4">
            {t("saas.trialDetails")}
          </Link>
        </div>
      )}
    </>
  );
}
