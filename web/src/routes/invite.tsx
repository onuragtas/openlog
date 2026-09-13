import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useRouter, useRouterState } from "@tanstack/react-router";
import { Loader2, UserPlus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { acceptInvitation, authConfigQuery, lookupInvitation } from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { ApiError } from "@/api/client";
import { AuthLayout } from "@/components/settings/AuthLayout";
import { LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/** Accepts an invitation link (/invite#token=oli_…). The token stays in the URL fragment. */
export function InvitePage() {
  const { t } = useTranslation();
  const id = useId();
  const router = useRouter();
  const queryClient = useQueryClient();
  const hash = useRouterState({ select: (s) => s.location.hash });
  const token = new URLSearchParams(hash.replace(/^#/, "")).get("token") ?? "";
  const lookup = useQuery({
    queryKey: ["invitation", token],
    queryFn: () => lookupInvitation(token),
    enabled: token !== "",
    retry: false,
    staleTime: Infinity,
  });
  const minLength = useQuery(authConfigQuery()).data?.password_min_length ?? 8;
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const info = lookup.data;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!info) return;
    if (!info.user_exists && password !== confirm) {
      setError(t("invite.mismatch"));
      return;
    }
    setPending(true);
    setError(null);
    try {
      const me = await acceptInvitation(token, password, name.trim());
      setSelectedOrg(me.organizations.find((o) => o.name === info.organization_name)?.id ?? null);
      queryClient.clear();
      router.history.push("/hosts");
    } catch (err) {
      setPending(false);
      if (!(err instanceof ApiError)) setError(t("login.unreachable"));
      else if (err.status === 401) setError(t("invite.wrongPassword"));
      else if (err.status === 404) setError(t("invite.invalid"));
      else if (err.status === 409) setError(t("invite.alreadyMember"));
      else setError(err.message);
    }
  };

  const failure = (message: string) => (
    <CardContent className="mt-4 flex flex-col gap-3">
      <p role="alert" className="text-sm text-destructive">
        {message}
      </p>
      <Link to="/login" className="text-sm text-primary underline-offset-4 hover:underline">
        {t("invite.toLogin")}
      </Link>
    </CardContent>
  );

  return (
    <AuthLayout>
      {!token ? (
        failure(t("invite.missingToken"))
      ) : lookup.isPending ? (
        <CardContent>
          <LoadingState />
        </CardContent>
      ) : !info ? (
        failure(t("invite.invalid"))
      ) : (
        <form onSubmit={submit} noValidate>
          <CardHeader>
            <CardTitle>
              <h1 className="text-lg">{t("invite.title", { org: info.organization_name })}</h1>
            </CardTitle>
            <CardDescription>{t("invite.description", { email: info.email, role: t(`settings.roles.${info.role}`) })}</CardDescription>
          </CardHeader>
          <CardContent className="mt-4 flex flex-col gap-3">
            <p className="text-sm">{info.user_exists ? t("invite.existingUser") : t("invite.newUser")}</p>
            <input type="email" name="username" autoComplete="username" value={info.email} readOnly hidden />
            {!info.user_exists && (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${id}-name`}>{t("invite.nameLabel")}</Label>
                <Input id={`${id}-name`} autoComplete="name" value={name} maxLength={200} onChange={(e) => setName(e.target.value)} />
              </div>
            )}
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-password`}>{t("invite.passwordLabel")}</Label>
              <Input
                id={`${id}-password`}
                type="password"
                autoComplete={info.user_exists ? "current-password" : "new-password"}
                autoFocus
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
              {!info.user_exists && <p className="text-xs text-muted-foreground">{t("invite.passwordHint", { min: minLength })}</p>}
            </div>
            {!info.user_exists && (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={`${id}-confirm`}>{t("invite.confirmLabel")}</Label>
                <Input id={`${id}-confirm`} type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
              </div>
            )}
            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}
          </CardContent>
          <CardFooter className="mt-4 justify-end">
            <Button type="submit" disabled={pending || password === ""}>
              {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <UserPlus aria-hidden="true" />}
              {pending ? t("invite.submitting") : t("invite.submit")}
            </Button>
          </CardFooter>
        </form>
      )}
    </AuthLayout>
  );
}
