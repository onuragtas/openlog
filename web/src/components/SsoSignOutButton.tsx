import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LogOut } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { continueSsoLogout, ssoLogout, ssoSessionQuery, type SsoLogoutResult } from "@/api/sso";

/**
 * "Sign out everywhere (IdP)": shown for single sign-on sessions whose identity provider session can be ended.
 * Ends the openlog session(s), then continues at the identity provider (redirect or HTTP-POST form), else goes to
 * the login page.
 */
export function SsoSignOutButton({
  className,
  onBeforeSignOut,
  goToLogin,
  assign = (url) => window.location.assign(url),
}: {
  className?: string;
  onBeforeSignOut?: () => void;
  goToLogin: () => void;
  assign?: (url: string) => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const me = useMe().data;
  const session = useQuery({ ...ssoSessionQuery(), enabled: me?.auth === "session" });
  const [pending, setPending] = useState(false);
  if (!session.data?.sso || !session.data.idp_logout) return null;

  return (
    <button
      type="button"
      disabled={pending}
      onClick={() => {
        onBeforeSignOut?.();
        setPending(true);
        void ssoLogout("/login")
          .catch((): SsoLogoutResult | null => null)
          .then((res) => {
            queryClient.clear();
            if (!res || !continueSsoLogout(res, assign)) goToLogin();
          });
      }}
      className={className}
    >
      <LogOut className="size-4 shrink-0" aria-hidden="true" />
      <span>{t("nav.signOutEverywhere")}</span>
    </button>
  );
}
