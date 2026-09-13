import { useQueryClient } from "@tanstack/react-query";
import { getRouteApi, useRouter } from "@tanstack/react-router";
import { Loader2, LogIn } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { login, meQuery } from "@/api/account";
import { ApiError } from "@/api/client";
import { AuthLayout } from "@/components/settings/AuthLayout";
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
      const target = search.redirect && search.redirect.startsWith("/") && !search.redirect.startsWith("//") ? search.redirect : "/hosts";
      router.history.push(target);
    } catch (err) {
      setPassword("");
      if (!(err instanceof ApiError)) setStatus("unreachable");
      else if (err.status === 401 || err.status === 400) setStatus("invalid");
      else if (err.status === 429) setStatus("rateLimited");
      else if (err.status === 404) setStatus("staticMode");
      else setStatus("unreachable");
    }
  };

  const messages: Partial<Record<Status, string>> = {
    invalid: t("login.invalid"),
    rateLimited: t("login.rateLimited"),
    unreachable: t("login.unreachable"),
    required: t("login.required"),
    staticMode: t("login.staticMode"),
  };
  const message = messages[status] ?? (search.expired ? t("login.sessionExpired") : null);
  const invalid = status === "invalid" || status === "required";

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
          {message && (
            <p id={`${id}-msg`} role="alert" className={status === "idle" ? "text-sm text-warning" : "text-sm text-destructive"}>
              {message}
            </p>
          )}
          <p className="text-xs text-muted-foreground">{t("login.note")}</p>
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
