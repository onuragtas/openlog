import { useQuery } from "@tanstack/react-query";
import { CheckCircle2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { verifySsoDomainEmail } from "@/api/sso";
import { LoadingState } from "@/components/StateViews";
import { CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { AuthLayout } from "./AuthLayout";

const linkClass = "text-sm text-primary underline-offset-4 hover:underline";

/** Opens a domain verification link (/sso/verify-domain#token=oldv_…). Needs no session. */
export function SsoVerifyDomainPage() {
  const { t } = useTranslation();
  // The token is in the fragment, so it never reaches server logs.
  const token = typeof window !== "undefined" ? (new URLSearchParams(window.location.hash.replace(/^#/, "")).get("token") ?? "") : "";
  const result = useQuery({
    queryKey: ["sso-verify-domain", token],
    queryFn: () => verifySsoDomainEmail(token),
    enabled: token !== "",
    retry: false,
    staleTime: Infinity,
    gcTime: Infinity,
  });

  return (
    <AuthLayout>
      <CardHeader>
        <CardTitle>
          <h1 className="text-lg">{t("sso.verifyDomain.title")}</h1>
        </CardTitle>
      </CardHeader>
      <CardContent className="mt-4 flex flex-col gap-3">
        {!token ? (
          <p role="alert" className="text-sm text-destructive">
            {t("sso.verifyDomain.missingToken")}
          </p>
        ) : result.isPending ? (
          <LoadingState label={t("sso.verifyDomain.checking")} />
        ) : result.isSuccess ? (
          <>
            <p role="status" className="flex items-center gap-2 text-sm">
              <CheckCircle2 className="size-4 text-success" aria-hidden="true" />
              {t("sso.verifyDomain.success", { domain: result.data })}
            </p>
            <a href="/settings/sso" className={linkClass}>
              {t("sso.verifyDomain.continue")}
            </a>
          </>
        ) : (
          <p role="alert" className="text-sm text-destructive">
            {t("sso.verifyDomain.invalid")}
          </p>
        )}
      </CardContent>
    </AuthLayout>
  );
}
