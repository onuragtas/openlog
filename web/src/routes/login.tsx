import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getRouteApi, Link, useRouter } from "@tanstack/react-router";
import { KeyRound, Loader2, LogIn } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { authConfigQuery, login, meQuery } from "@/api/account";
import { ApiError } from "@/api/client";
import { AuthLayout } from "@/components/settings/AuthLayout";
import { SsoLogoutNotice } from "@/components/settings/SsoLogoutNotice";
import { SsoSignIn } from "@/components/settings/SsoSignIn";
import { Button } from "@/components/ui/button";
import { CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const route = getRouteApi("/login");

type Status = "idle" | "checking" | "invalid" | "rateLimited" | "unreachable" | "required" | "staticMode";

export function LoginPage() {
  const { t } = useTranslation();
  const search = route.useSearch();
  const router = useRouter();
  const queryClient = useQueryClient();
  const id = useId();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [status, setStatus] = useState<Status>("idle");
  // Single sign-on (components/settings/SsoSignIn): chosen by the user, or required by the organization.
  const [mode, setMode] = useState<"password" | "sso">("password");
  const [ssoNotice, setSsoNotice] = useState<string | null>(null);
  const authConfig = useQuery(authConfigQuery()).data;
  const signupEnabled = authConfig?.signup_enabled === true;
  const ssoEnabled = authConfig?.sso_enabled === true;
  const target = search.redirect && search.redirect.startsWith("/") && !search.redirect.startsWith("//") ? search.redirect : "/hosts";

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (email.trim() === "" || password === "") {
      setStatus("required");
      return;
    }
    setStatus("checking");
    try {
      const me = await login(email.trim(), password);
      queryClient.clear();
      queryClient.setQueryData(meQuery().queryKey, me);
      router.history.push(target);
    } catch (err) {
      setPassword("");
      if (!(err instanceof ApiError)) setStatus("unreachable");
      else if (err.status === 401 || err.status === 400) setStatus("invalid");
      else if (err.status === 403) {
        // Correct password, but every organization of the user enforces single sign-on.
        setStatus("idle");
        setSsoNotice(t("sso.login.required"));
        setMode("sso");
      } else if (err.status === 429) setStatus("rateLimited");
      else if (err.status === 404) setStatus("staticMode");
      else setStatus("unreachable");
    }
  };

  if (mode === "sso") {
    return (
      <AuthLayout>
        <SsoSignIn
          initialEmail={email.trim()}
          redirect={target}
          notice={ssoNotice}
          onBack={() => {
            setMode("password");
            setSsoNotice(null);
          }}
        />
      </AuthLayout>
    );
  }

  const messages: Partial<Record<Status, string>> = {
    invalid: t("login.invalid"),
    rateLimited: t("login.rateLimited"),
    unreachable: t("login.unreachable"),
    required: t("login.required"),
    staticMode: t("login.staticMode"),
  };
  const ssoError = search.sso_error ? t(`sso.login.errors.${search.sso_error}`) : null;
  const message = messages[status] ?? ssoError ?? (search.expired ? t("login.sessionExpired") : null);
  const invalid = status === "invalid" || status === "required";
  const warningTone = status === "idle" && !ssoError;

  return (
    <AuthLayout>
      <form onSubmit={submit} noValidate>
        <CardHeader>
          <div className="mb-2 flex items-center gap-2">
            <img src="/favicon.svg" alt="" className="size-8" />
            <span className="text-lg font-semibold">{t("app.name")}</span>
          </div>
          <CardTitle>
            <h1 className="text-lg">{t("login.title")}</h1>
          </CardTitle>
          <CardDescription>{t("login.description")}</CardDescription>
        </CardHeader>
        <CardContent className="mt-4 flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-email`}>{t("login.emailLabel")}</Label>
            <Input
              id={`${id}-email`}
              name="email"
              type="email"
              autoComplete="username"
              autoFocus
              value={email}
              placeholder={t("login.emailPlaceholder")}
              aria-invalid={invalid}
              aria-describedby={message ? `${id}-msg` : undefined}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-password`}>{t("login.passwordLabel")}</Label>
            <Input
              id={`${id}-password`}
              name="password"
              type="password"
              autoComplete="current-password"
              value={password}
              aria-invalid={invalid}
              aria-describedby={message ? `${id}-msg` : undefined}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          {message ? (
            <p id={`${id}-msg`} role="alert" className={warningTone ? "text-sm text-warning" : "text-sm text-destructive"}>
              {message}
            </p>
          ) : (
            <SsoLogoutNotice status={search.sso_logout} />
          )}
          {ssoEnabled && (
            <Button type="button" variant="outline" onClick={() => setMode("sso")}>
              <KeyRound aria-hidden="true" />
              {t("sso.login.button")}
            </Button>
          )}
          {signupEnabled ? (
            <p className="text-sm text-muted-foreground">
              {t("login.signupPrompt")}{" "}
              <Link to="/signup" className="text-primary underline-offset-4 hover:underline">
                {t("login.signupLink")}
              </Link>
            </p>
          ) : (
            <p className="text-xs text-muted-foreground">{t("login.note")}</p>
          )}
        </CardContent>
        <CardFooter className="mt-4 justify-end">
          <Button type="submit" disabled={status === "checking"}>
            {status === "checking" ? <Loader2 className="animate-spin" aria-hidden="true" /> : <LogIn aria-hidden="true" />}
            {status === "checking" ? t("login.checking") : t("login.submit")}
          </Button>
        </CardFooter>
      </form>
    </AuthLayout>
  );
}
