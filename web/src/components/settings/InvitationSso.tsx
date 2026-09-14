import { KeyRound } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { InvitationSso as InvitationSsoInfo } from "@/api/sso";
import { Button } from "@/components/ui/button";
import { CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { SsoSignIn } from "./SsoSignIn";

/**
 * Invitation to an address in a domain claimed by single sign-on (D-089): no password form. Invitations of the
 * claiming organization are accepted by signing in with SSO. Router-free: the page passes the login link in.
 */
export function InvitationSso({
  organizationName,
  email,
  roleLabel,
  sso,
  loginLink,
  navigate,
}: {
  organizationName: string;
  email: string;
  roleLabel: string;
  sso: InvitationSsoInfo;
  loginLink: ReactNode;
  navigate?: (url: string) => void;
}) {
  const { t } = useTranslation();
  const [signIn, setSignIn] = useState(false);

  if (signIn) {
    return <SsoSignIn initialEmail={email} notice={t("sso.login.inviteSameOrg")} onBack={() => setSignIn(false)} navigate={navigate} />;
  }
  return (
    <div data-testid="invitation-sso">
      <CardHeader>
        <CardTitle>
          <h1 className="text-lg">{t("invite.title", { org: organizationName })}</h1>
        </CardTitle>
        <CardDescription>{t("invite.description", { email, role: roleLabel })}</CardDescription>
      </CardHeader>
      <CardContent className="mt-4 flex flex-col gap-3">
        <p role="status" className="text-sm font-medium break-words">
          {t("sso.login.inviteClaimed", { org: sso.organization_name, email })}
        </p>
        <p className="text-sm text-muted-foreground">{sso.same_organization ? t("sso.login.inviteSameOrg") : t("sso.login.inviteOtherOrg")}</p>
        {!sso.same_organization && loginLink}
      </CardContent>
      {sso.same_organization && (
        <CardFooter className="mt-4 justify-end">
          <Button type="button" onClick={() => setSignIn(true)}>
            <KeyRound aria-hidden="true" />
            {t("sso.login.button")}
          </Button>
        </CardFooter>
      )}
    </div>
  );
}
