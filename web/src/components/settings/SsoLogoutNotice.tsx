import { useTranslation } from "react-i18next";
import type { SsoLogoutStatus } from "@/api/sso";

/** Login page message after "Sign out everywhere" returns from the identity provider (?sso_logout=ok|partial). */
export function SsoLogoutNotice({ status, id }: { status: SsoLogoutStatus | undefined; id?: string }) {
  const { t } = useTranslation();
  if (!status) return null;
  return (
    <p id={id} role="status" className={status === "ok" ? "text-sm text-success" : "text-sm text-warning"}>
      {status === "ok" ? t("sso.login.logoutOk") : t("sso.login.logoutPartial")}
    </p>
  );
}
