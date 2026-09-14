import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

export type CaptchaProvider = "turnstile" | "hcaptcha";

interface CaptchaApi {
  render(el: HTMLElement, options: Record<string, unknown>): string | number;
  remove?(id: string | number): void;
}

declare global {
  interface Window {
    turnstile?: CaptchaApi;
    hcaptcha?: CaptchaApi;
  }
}

const SCRIPTS: Record<CaptchaProvider, string> = {
  turnstile: "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit",
  hcaptcha: "https://js.hcaptcha.com/1/api.js?render=explicit&recaptchacompat=off",
};

const loading = new Map<string, Promise<void>>();

function loadScript(src: string): Promise<void> {
  let p = loading.get(src);
  if (!p) {
    p = new Promise<void>((resolve, reject) => {
      const s = document.createElement("script");
      s.src = src;
      s.async = true;
      s.defer = true;
      s.onload = () => resolve();
      s.onerror = () => {
        loading.delete(src);
        reject(new Error("captcha script failed to load"));
      };
      document.head.appendChild(s);
    });
    loading.set(src, p);
  }
  return p;
}

async function waitForApi(provider: CaptchaProvider, timeoutMs = 10_000): Promise<CaptchaApi> {
  const start = Date.now();
  for (;;) {
    const api = window[provider];
    if (api) return api;
    if (Date.now() - start > timeoutMs) throw new Error("captcha API not available");
    await new Promise((r) => setTimeout(r, 100));
  }
}

/**
 * Cloudflare Turnstile / hCaptcha widget for sign-up (GET /auth/config → captcha). The token is
 * verified server-side; `onToken(null)` when it expires or fails. Remount (change `key`) to reset.
 */
export function Captcha({ provider, siteKey, onToken }: { provider: CaptchaProvider; siteKey: string; onToken: (token: string | null) => void }) {
  const { t } = useTranslation();
  const ref = useRef<HTMLDivElement>(null);
  const tokenCb = useRef(onToken);
  const [state, setState] = useState<"loading" | "ready" | "failed">("loading");

  useEffect(() => {
    tokenCb.current = onToken;
  }, [onToken]);

  useEffect(() => {
    let cancelled = false;
    let widget: { api: CaptchaApi; id: string | number } | null = null;
    loadScript(SCRIPTS[provider])
      .then(() => waitForApi(provider))
      .then((api) => {
        if (cancelled || !ref.current) return;
        const id = api.render(ref.current, {
          sitekey: siteKey,
          callback: (token: string) => tokenCb.current(token),
          "expired-callback": () => tokenCb.current(null),
          "error-callback": () => tokenCb.current(null),
        });
        widget = { api, id };
        setState("ready");
      })
      .catch(() => {
        if (!cancelled) setState("failed");
      });
    return () => {
      cancelled = true;
      if (widget) widget.api.remove?.(widget.id);
    };
  }, [provider, siteKey]);

  return (
    <div className="flex min-h-16 flex-col gap-1">
      <div ref={ref} data-testid="captcha" />
      {state === "loading" && <p className="text-xs text-muted-foreground">{t("signup.captchaLoading")}</p>}
      {state === "failed" && (
        <p role="alert" className="text-xs text-destructive">
          {t("signup.captchaFailed")}
        </p>
      )}
    </div>
  );
}
