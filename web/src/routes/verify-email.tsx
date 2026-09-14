import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { CheckCircle2 } from "lucide-react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { meQuery, verifyEmail } from "@/api/account";
import { AuthLayout } from "@/components/settings/AuthLayout";
import { LoadingState } from "@/components/StateViews";
import { CardContent, CardHeader, CardTitle } from "@/components/ui/card";

const linkClass = "text-sm text-primary underline-offset-4 hover:underline";

/** Opens an e-mail verification link (/verify-email#token=olv_…). Works with or without a session. */
export function VerifyEmailPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const hash = useRouterState({ select: (s) => s.location.hash });
  const token = new URLSearchParams(hash.replace(/^#/, "")).get("token") ?? "";
  const result = useQuery({
    queryKey: ["verify-email", token],
    queryFn: async () => {
      await verifyEmail(token);
      return true;
    },
    enabled: token !== "",
    retry: false,
    staleTime: Infinity,
    gcTime: Infinity,
  });

  useEffect(() => {
    if (result.isSuccess) void queryClient.invalidateQueries({ queryKey: meQuery().queryKey });
  }, [result.isSuccess, queryClient]);

  return (
    <AuthLayout>
      <CardHeader>
        <CardTitle>
          <h1 className="text-lg">{t("verifyEmail.title")}</h1>
        </CardTitle>
      </CardHeader>
      <CardContent className="mt-4 flex flex-col gap-3">
        {!token ? (
          <p role="alert" className="text-sm text-destructive">
            {t("verifyEmail.missingToken")}
          </p>
        ) : result.isPending ? (
          <LoadingState label={t("verifyEmail.checking")} />
        ) : result.isSuccess ? (
          <>
            <p role="status" className="flex items-center gap-2 text-sm">
              <CheckCircle2 className="size-4 text-success" aria-hidden="true" />
              {t("verifyEmail.success")}
            </p>
            <Link to="/hosts" className={linkClass}>
              {t("verifyEmail.continue")}
            </Link>
          </>
        ) : (
          <>
            <p role="alert" className="text-sm text-destructive">
              {t("verifyEmail.invalid")}
            </p>
            <Link to="/login" className={linkClass}>
              {t("invite.toLogin")}
            </Link>
          </>
        )}
      </CardContent>
    </AuthLayout>
  );
}
