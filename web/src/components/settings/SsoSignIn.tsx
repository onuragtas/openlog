import { KeyRound, Loader2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import { discoverSso, startSsoLogin } from "@/api/sso";
import { Button } from "@/components/ui/button";
import { CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type Status = "idle" | "checking" | "redirecting" | "notConfigured" | "rateLimited" | "unavailable" | "required";

/** "Sign in with SSO": discovers the organization of the e-mail domain and continues at its identity provider. */
export function SsoSignIn({
  initialEmail = "",
  redirect,
  notice,
  onBack,
  navigate = (url) => window.location.assign(url),
}: {
  initialEmail?: string;
  redirect?: string;
  /** Message shown above the form (e.g. "Your organization requires single sign-on"). */
  notice?: string | null;
  onBack: () => void;
  navigate?: (url: string) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const [email, setEmail] = useState(initialEmail);
  const [status, setStatus] = useState<Status>("idle");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!/^[^@\s]+@[^@\s]+$/.test(email.trim())) {
      setStatus("required");
      return;
    }
    setStatus("checking");
    try {
      const d = await discoverSso(email.trim());
      if (!d.sso) {
        setStatus("notConfigured");
        return;
      }
      const url = await startSsoLogin(email.trim(), redirect);
      setStatus("redirecting");
      navigate(url);
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) setStatus("rateLimited");
      else if (err instanceof ApiError && err.status === 404) setStatus("notConfigured");
      else if (err instanceof ApiError && err.status === 400) setStatus("required");
      else setStatus("unavailable");
    }
  };

  const messages: Partial<Record<Status, string>> = {
    notConfigured: t("sso.login.notConfigured"),
    rateLimited: t("sso.login.rateLimited"),
    unavailable: t("sso.login.unavailable"),
    required: t("login.required"),
  };
  const message = messages[status];
  const busy = status === "checking" || status === "redirecting";

  return (
    <form onSubmit={submit} noValidate data-testid="sso-sign-in">
      <CardHeader>
        <div className="mb-2 flex items-center gap-2">
          <img src="/favicon.svg" alt="" className="size-8" />
          <span className="text-lg font-semibold">{t("app.name")}</span>
        </div>
        <CardTitle>
          <h1 className="text-lg">{t("sso.login.title")}</h1>
        </CardTitle>
        <CardDescription>{t("sso.login.description")}</CardDescription>
      </CardHeader>
      <CardContent className="mt-4 flex flex-col gap-3">
        {notice && (
          <p role="status" className="text-sm text-warning">
            {notice}
          </p>
        )}
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-email`}>{t("sso.login.emailLabel")}</Label>
          <Input
            id={`${id}-email`}
            type="email"
            name="email"
            autoComplete="username"
            autoFocus
            value={email}
            placeholder={t("login.emailPlaceholder")}
            aria-invalid={status === "required"}
            aria-describedby={message ? `${id}-msg` : undefined}
            onChange={(e) => setEmail(e.target.value)}
          />
        </div>
        {message && (
          <p id={`${id}-msg`} role="alert" className="text-sm text-destructive">
            {message}
          </p>
        )}
      </CardContent>
      <CardFooter className="mt-4 flex flex-wrap justify-between gap-2">
        <Button type="button" variant="ghost" onClick={onBack}>
          {t("sso.login.back")}
        </Button>
        <Button type="submit" disabled={busy}>
          {busy ? <Loader2 className="animate-spin" aria-hidden="true" /> : <KeyRound aria-hidden="true" />}
          {status === "redirecting" ? t("sso.login.redirecting") : t("sso.login.continue")}
        </Button>
      </CardFooter>
    </form>
  );
}
