import { useMutation, useQuery } from "@tanstack/react-query";
import { Mail } from "lucide-react";
import { useTranslation } from "react-i18next";
import { authConfigQuery, resendVerification, useMe } from "@/api/account";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";

/** Shown to signed-up users who have not confirmed their e-mail address (they cannot create keys or invite yet). */
export function EmailVerificationBanner() {
  const { t } = useTranslation();
  const me = useMe().data;
  const config = useQuery(authConfigQuery()).data;
  const resend = useMutation({ mutationFn: resendVerification });

  if (!me?.user || me.user.email_verified || !config?.email_verification_required) return null;
  const failure = resend.error instanceof ApiError ? resend.error.message : resend.error ? String(resend.error) : null;
  return (
    <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-warning/60 bg-warning/10 px-4 py-2 text-sm">
      <Mail className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1 break-words">{t("verifyEmail.bannerText", { email: me.user.email })}</span>
      {resend.isSuccess ? (
        <span className="text-muted-foreground">{t("verifyEmail.resent")}</span>
      ) : (
        config.email_enabled && (
          <Button type="button" size="sm" variant="outline" disabled={resend.isPending} onClick={() => resend.mutate()}>
            {resend.isPending ? t("verifyEmail.resending") : t("verifyEmail.resend")}
          </Button>
        )
      )}
      {failure && <span className="w-full text-destructive">{t("verifyEmail.resendFailed", { message: failure })}</span>}
    </div>
  );
}
