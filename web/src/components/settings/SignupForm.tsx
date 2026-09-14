import { useQuery } from "@tanstack/react-query";
import { KeyRound, Loader2, UserPlus } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { authConfigQuery, signup, type Me } from "@/api/account";
import { ApiError } from "@/api/client";
import { discoverSso } from "@/api/sso";
import { LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Captcha, type CaptchaProvider } from "./Captcha";
import { SsoSignIn } from "./SsoSignIn";

/** Self-service sign-up (POST /auth/signup). Router-free: the route passes navigation in. */
export function SignupForm({
  onSignedUp,
  loginLink,
  navigate,
}: {
  onSignedUp: (me: Me) => void;
  loginLink: ReactNode;
  /** External navigation of "Continue with SSO" (default window.location.assign). */
  navigate?: (url: string) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const config = useQuery(authConfigQuery());
  const [name, setName] = useState("");
  const [org, setOrg] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [captchaToken, setCaptchaToken] = useState<string | null>(null);
  const [captchaKey, setCaptchaKey] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  // Claimed-domain redirection (D-089): the organization whose single sign-on this e-mail domain uses.
  const [claimedBy, setClaimedBy] = useState<string | null>(null);
  const [ssoMode, setSsoMode] = useState(false);

  if (ssoMode && claimedBy !== null) {
    return <SsoSignIn initialEmail={email.trim()} notice={t("sso.login.signupClaimed", { org: claimedBy })} onBack={() => setSsoMode(false)} navigate={navigate} />;
  }

  if (config.isPending) {
    return (
      <CardContent>
        <LoadingState />
      </CardContent>
    );
  }
  const cfg = config.data;
  const minLength = cfg?.password_min_length ?? 8;
  const captcha = cfg?.captcha ?? null;

  if (!cfg?.signup_enabled) {
    return (
      <CardContent className="mt-4 flex flex-col gap-3">
        <p role="alert" className="text-sm">
          {t("signup.disabled")}
        </p>
        {loginLink}
      </CardContent>
    );
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setClaimedBy(null);
    if (email.trim() === "" || org.trim() === "" || password === "") {
      setError(t("signup.required"));
      return;
    }
    if (password !== confirm) {
      setError(t("signup.mismatch"));
      return;
    }
    if (password.length < minLength) {
      setError(t("signup.passwordHint", { min: minLength }));
      return;
    }
    if (captcha && !captchaToken) {
      setError(t("signup.captchaRequired"));
      return;
    }
    setPending(true);
    try {
      const me = await signup({
        email: email.trim(),
        password,
        name: name.trim(),
        organization_name: org.trim(),
        ...(captchaToken ? { captcha_token: captchaToken } : {}),
      });
      onSignedUp(me);
    } catch (err) {
      setPending(false);
      // A CAPTCHA token is single-use: get a fresh one.
      setCaptchaToken(null);
      setCaptchaKey((k) => k + 1);
      if (!(err instanceof ApiError)) setError(t("login.unreachable"));
      else if (err.status === 409 && err.code === "failed_precondition") {
        // The e-mail domain signs in with an organization's single sign-on: no password account.
        const d = await discoverSso(email.trim()).catch(() => null);
        if (d?.sso) setClaimedBy(d.organization_name ?? "");
        else setError(err.message);
      } else if (err.status === 409) setError(t("signup.emailTaken"));
      else if (err.status === 429) setError(t("signup.rateLimited"));
      else if (err.status === 403) setError(t("signup.disabled"));
      else setError(err.message);
    }
  };

  return (
    <form onSubmit={submit} noValidate>
      <CardHeader>
        <div className="mb-2 flex items-center gap-2">
          <img src="/favicon.svg" alt="" className="size-8" />
          <span className="text-lg font-semibold">{t("app.name")}</span>
        </div>
        <CardTitle>
          <h1 className="text-lg">{t("signup.title")}</h1>
        </CardTitle>
        <CardDescription>{t("signup.description")}</CardDescription>
      </CardHeader>
      <CardContent className="mt-4 flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-name`}>{t("signup.nameLabel")}</Label>
          <Input id={`${id}-name`} autoComplete="name" maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-org`}>{t("signup.orgLabel")}</Label>
          <Input id={`${id}-org`} autoComplete="organization" maxLength={200} placeholder={t("signup.orgPlaceholder")} value={org} onChange={(e) => setOrg(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-email`}>{t("login.emailLabel")}</Label>
          <Input id={`${id}-email`} type="email" autoComplete="username" placeholder={t("login.emailPlaceholder")} value={email} onChange={(e) => setEmail(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-password`}>{t("login.passwordLabel")}</Label>
          <Input
            id={`${id}-password`}
            type="password"
            autoComplete="new-password"
            minLength={minLength}
            aria-describedby={`${id}-hint`}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <p id={`${id}-hint`} className="text-xs text-muted-foreground">
            {t("signup.passwordHint", { min: minLength })}
          </p>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-confirm`}>{t("signup.confirmLabel")}</Label>
          <Input id={`${id}-confirm`} type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        </div>
        {captcha && <Captcha key={captchaKey} provider={captcha.provider as CaptchaProvider} siteKey={captcha.site_key} onToken={setCaptchaToken} />}
        {cfg.email_verification_required && <p className="text-xs text-muted-foreground">{t("signup.verificationNote")}</p>}
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        {claimedBy !== null && (
          <div className="flex flex-col items-start gap-2 rounded-lg border border-warning/60 bg-warning/10 p-3">
            <p role="alert" className="text-sm">
              {t("sso.login.signupClaimed", { org: claimedBy })}
            </p>
            <Button type="button" variant="outline" size="sm" onClick={() => setSsoMode(true)}>
              <KeyRound aria-hidden="true" />
              {t("sso.login.button")}
            </Button>
          </div>
        )}
        <p className="text-sm text-muted-foreground">
          {t("signup.haveAccount")} {loginLink}
        </p>
      </CardContent>
      <CardFooter className="mt-4 justify-end">
        <Button type="submit" disabled={pending}>
          {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <UserPlus aria-hidden="true" />}
          {pending ? t("signup.submitting") : t("signup.submit")}
        </Button>
      </CardFooter>
    </form>
  );
}
