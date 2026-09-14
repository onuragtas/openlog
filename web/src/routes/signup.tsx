import { useQueryClient } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { meQuery } from "@/api/account";
import { AuthLayout } from "@/components/settings/AuthLayout";
import { SignupForm } from "@/components/settings/SignupForm";

/** Self-service sign-up (/signup), only useful with OPENLOG_SIGNUP_ENABLED=true. */
export function SignupPage() {
  const { t } = useTranslation();
  const router = useRouter();
  const queryClient = useQueryClient();
  return (
    <AuthLayout>
      <SignupForm
        onSignedUp={(me) => {
          queryClient.clear();
          queryClient.setQueryData(meQuery().queryKey, me);
          router.history.push("/hosts");
        }}
        loginLink={
          <Link to="/login" className="text-sm text-primary underline-offset-4 hover:underline">
            {t("signup.toLogin")}
          </Link>
        }
      />
    </AuthLayout>
  );
}
