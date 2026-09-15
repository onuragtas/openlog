import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, ArrowRight, BookOpen, Check, Eye, EyeOff } from "lucide-react";
import { Fragment, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { baselineQuery, type Onboarding } from "@/api/onboarding";
import { Button } from "@/components/ui/button";
import { buildInstallCommands, DEFAULT_OPTIONS, findTarget, targetNeedsKey, type InstallOptions, type InstallTarget } from "@/lib/install-commands";
import { initialKeyChoice, keyChoiceReady, keyForCommands, tDynamic, type KeyChoice } from "@/lib/onboarding-key";
import { cn } from "@/lib/utils";
import { CommandBlockView } from "./CommandBlockView";
import { LicenseKeyStep } from "./LicenseKeyStep";
import { OptionsStep } from "./OptionsStep";
import { VerifyStep } from "./VerifyStep";

const STEPS = ["key", "options", "install", "verify"] as const;

/**
 * Guided setup of one Add data card: license key → options → commands → verification. The key value stays in this
 * component's state (never in the URL or storage).
 */
export function InstallFlow({
  target,
  onboarding,
  initial,
  intervalMs,
  timeoutMs,
}: {
  target: InstallTarget;
  onboarding: Onboarding;
  initial?: Partial<InstallOptions>;
  intervalMs?: number;
  timeoutMs?: number;
}) {
  const { t, i18n } = useTranslation();
  const [step, setStep] = useState(0);
  const [startedAt] = useState(() => Date.now());
  const [keyChoice, setKeyChoice] = useState<KeyChoice>(() => initialKeyChoice(onboarding.features.can_create_license_keys));
  const [options, setOptions] = useState<InstallOptions>(() => ({ ...DEFAULT_OPTIONS, ...initial }));
  const [revealed, setRevealed] = useState(false);
  const needsKey = targetNeedsKey(target.id);
  const ready = !needsKey || keyChoiceReady(keyChoice);
  const licenseKey = needsKey ? keyForCommands(keyChoice) : "";
  const title = tDynamic(t, `addData.targets.${target.id}.title`);

  // Snapshot of what already reports, taken when the flow opens (VerifyStep reads the same query).
  const baselineKind = target.verify === "host" || target.verify === "kubernetes" || target.verify === "apm" || target.verify === "otel" ? target.verify : null;
  useQuery({ ...baselineQuery(baselineKind ?? "host", startedAt), enabled: baselineKind !== null });

  const commands = useMemo(() => buildInstallCommands(target.id, { ...options, licenseKey }, onboarding), [target.id, options, licenseKey, onboarding]);
  const required = (target.requires ?? []).map((r) => findTarget(r)).filter((r): r is InstallTarget => !!r);
  const requiredNames = new Intl.ListFormat(i18n.resolvedLanguage ?? "en", { type: "disjunction" }).format(
    required.map((r) => tDynamic(t, `addData.targets.${r.id}.title`)),
  );
  const stepTitle = (i: number) => tDynamic(t, `addData.steps.${STEPS[i]}`);
  const reachable = (i: number) => i === 0 || ready;
  const endpoints = [
    { label: t("addData.install.endpoint"), ep: onboarding.otlp_http },
    ...(target.options.includes("protocol") && options.protocol === "grpc" ? [{ label: t("addData.install.grpcEndpoint"), ep: onboarding.otlp_grpc }] : []),
  ];

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <nav aria-label={t("addData.steps.label")}>
        <ol className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
          {STEPS.map((s, i) => (
            <li key={s} className="min-w-0">
              <button
                type="button"
                disabled={!reachable(i)}
                aria-current={step === i ? "step" : undefined}
                onClick={() => setStep(i)}
                className={cn(
                  "flex min-h-10 w-full min-w-0 items-center gap-2 rounded-md border px-2.5 py-1.5 text-left text-sm disabled:cursor-not-allowed disabled:opacity-50",
                  step === i ? "border-primary bg-primary/5 font-medium" : "hover:bg-accent",
                )}
              >
                <span
                  className={cn(
                    "grid size-6 shrink-0 place-items-center rounded-full border text-xs",
                    step === i && "border-primary bg-primary text-primary-foreground",
                    step > i && "border-success bg-success/15 text-success-text",
                  )}
                  aria-hidden="true"
                >
                  {step > i ? <Check className="size-3.5" /> : i + 1}
                </span>
                <span className="truncate">{stepTitle(i)}</span>
              </button>
            </li>
          ))}
        </ol>
      </nav>

      <section aria-labelledby="add-data-step" className="flex min-w-0 flex-col gap-4 rounded-xl border bg-card p-4 md:p-5" data-testid={`add-data-step-${STEPS[step]}`}>
        <h2 id="add-data-step" className="text-base font-semibold">
          {step === 0 ? t("addData.key.title") : step === 1 ? t("addData.options.title") : step === 2 ? t("addData.install.title") : t("addData.verify.title")}
        </h2>

        {step === 0 && <LicenseKeyStep onboarding={onboarding} targetTitle={title} needed={needsKey} value={keyChoice} onChange={setKeyChoice} />}

        {step === 1 && <OptionsStep target={target} value={options} onChange={setOptions} />}

        {step === 2 && (
          <>
            {required.length > 0 && (
              <p className="rounded-lg border bg-muted/40 p-3 text-sm">
                {t("addData.install.requires", { name: requiredNames })}{" "}
                {required.map((r, i) => (
                  <Fragment key={r.id}>
                    {i > 0 && " · "}
                    <Link to="/add-data/$" params={{ _splat: r.id }} className="text-primary hover:underline">
                      {tDynamic(t, `addData.targets.${r.id}.title`)}
                    </Link>
                  </Fragment>
                ))}
              </p>
            )}
            {needsKey && (
              <dl className="grid min-w-0 grid-cols-1 gap-2 text-sm sm:grid-cols-[auto_1fr] sm:gap-x-4">
                {endpoints.map(({ label, ep }) => (
                  <div key={label} className="contents">
                    <dt className="text-muted-foreground">{label}</dt>
                    <dd className="min-w-0">
                      <code className="font-mono text-xs break-all">{ep.url}</code>
                      {ep.source !== "configured" && (
                        <p className="text-xs text-muted-foreground">{t("addData.install.derived", { source: tDynamic(t, `addData.install.sources.${ep.source}`) })}</p>
                      )}
                    </dd>
                  </div>
                ))}
              </dl>
            )}
            {licenseKey && commands.blocks.some((b) => b.containsKey) && (
              <Button type="button" variant="outline" size="sm" className="w-fit" aria-pressed={revealed} onClick={() => setRevealed((r) => !r)}>
                {revealed ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
                {revealed ? t("addData.install.hideKey") : t("addData.install.showKey")}
              </Button>
            )}
            <ol className="flex min-w-0 flex-col gap-3">
              {commands.blocks.map((b) => (
                <li key={b.id} className="min-w-0">
                  <CommandBlockView block={b} title={tDynamic(t, `addData.blocks.${b.label}`)} licenseKey={licenseKey} revealed={revealed} />
                </li>
              ))}
            </ol>
            {commands.notes.length > 0 && (
              <ul data-testid="install-notes" className="flex list-disc flex-col gap-1 pl-5 text-sm break-words text-muted-foreground">
                {commands.notes.map((n) => (
                  <li key={n}>{tDynamic(t, `addData.notes.${n}`, { origins: onboarding.cors_allowed_origins.join(", ") })}</li>
                ))}
              </ul>
            )}
            <a href={target.docs} target="_blank" rel="noreferrer" className="flex w-fit items-center gap-1.5 text-sm text-primary hover:underline">
              <BookOpen className="size-4" aria-hidden="true" />
              {t("addData.docs")}
            </a>
          </>
        )}

        {step === 3 && <VerifyStep target={target} options={options} startedAt={startedAt} endpoint={onboarding.otlp_http.url} intervalMs={intervalMs} timeoutMs={timeoutMs} />}
      </section>

      <div className="flex flex-wrap items-center justify-between gap-2">
        {step > 0 ? (
          <Button type="button" variant="outline" onClick={() => setStep((s) => s - 1)}>
            <ArrowLeft aria-hidden="true" />
            {t("addData.steps.back")}
          </Button>
        ) : (
          <span />
        )}
        {step < 2 && (
          <Button type="button" disabled={!ready} onClick={() => setStep((s) => s + 1)}>
            {t("addData.steps.next")}
            <ArrowRight aria-hidden="true" />
          </Button>
        )}
        {step === 2 && (
          <Button type="button" onClick={() => setStep(3)}>
            {t("addData.install.ran")}
            <ArrowRight aria-hidden="true" />
          </Button>
        )}
      </div>
    </div>
  );
}
