// Of the sessions that did the first step, how many reached each of the next ones in order (rum.md §2.8).
//
// The steps are custom events the application sends itself (`openlog.track("checkout_started")`), so they
// are typed in rather than picked: openlog cannot know the names a product uses before it sees them, and a
// list of every event name in the range would be a different question with a different cost.
//
// The order is enforced by the server, not assumed here: a session that reached checkout before it saw the
// cart has not completed the funnel, and counting each step on its own would say it had.
import { useQuery } from "@tanstack/react-query";
import { Filter } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { rumFunnelQuery, type RumFunnelStep } from "@/api/rum";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { formatNumber } from "@/lib/format";
import { formatShare } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

/** What the endpoint takes: between two and six steps. */
export const MAX_FUNNEL_STEPS = 6;
const WINDOWS = ["5m", "15m", "30m", "1h", "6h", "24h"] as const;

/** One step as a bar of the first step's sessions, plus what it lost against the step before it. */
function Step({ step, previous, index }: { step: RumFunnelStep; previous: RumFunnelStep | undefined; index: number }) {
  const { t, i18n } = useTranslation();
  // The drop-off that matters is the one against the step before, not against the start: a funnel that
  // loses 90% at step two and nothing after it is a different problem from one that leaks everywhere.
  const dropped = previous && previous.sessions > 0 ? 1 - step.sessions / previous.sessions : 0;
  return (
    <li className="flex flex-col gap-1 rounded-xl border bg-card p-3" data-testid="rum-funnel-step">
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="text-xs text-muted-foreground tabular-nums">{index + 1}.</span>
        <span className="font-mono text-sm break-all">{step.step}</span>
        <span className="ml-auto text-sm font-semibold tabular-nums">{formatShare(step.rate)}</span>
      </div>
      <div className="h-2 overflow-hidden rounded-full bg-muted" aria-hidden="true">
        <div className="h-full bg-primary" style={{ width: `${Math.max(0, Math.min(1, step.rate)) * 100}%` }} />
      </div>
      <p className="text-xs text-muted-foreground">
        {t("rum.funnel.sessions", { n: formatNumber(step.sessions, i18n.language) })}
        {previous && dropped > 0 && <span className="text-destructive"> · {t("rum.funnel.dropped", { share: formatShare(dropped) })}</span>}
      </p>
    </li>
  );
}

export function RumFunnelTab({
  range,
  app,
  environment,
  steps,
  funnelWindow,
  onChange,
}: {
  range: RangeSpec;
  app: string;
  environment?: string;
  steps: string[];
  funnelWindow: string;
  onChange: (patch: { steps?: string[]; funnelWindow?: string }) => void;
}) {
  const { t, i18n } = useTranslation();
  // Typed but not yet applied: every keystroke would otherwise be a query over the raw spans.
  const [draft, setDraft] = useState(steps.join(", "));
  const funnel = useQuery(rumFunnelQuery(range, app, steps, funnelWindow, environment));

  const apply = () => {
    const next = draft
      .split(",")
      .map((s) => s.trim())
      .filter((s) => s !== "")
      .slice(0, MAX_FUNNEL_STEPS);
    onChange({ steps: next });
  };

  return (
    <div className="flex flex-col gap-3">
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          apply();
        }}
      >
        <div className="flex min-w-64 flex-1 flex-col gap-1">
          <Label htmlFor="rum-funnel-steps" className="text-xs text-muted-foreground">
            {t("rum.funnel.stepsLabel")}
          </Label>
          <Input id="rum-funnel-steps" value={draft} placeholder={t("rum.funnel.stepsPlaceholder")} onChange={(e) => setDraft(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="rum-funnel-window" className="text-xs text-muted-foreground">
            {t("rum.funnel.windowLabel")}
          </Label>
          <NativeSelect id="rum-funnel-window" className="w-32" value={funnelWindow} onChange={(e) => onChange({ funnelWindow: e.target.value })}>
            {WINDOWS.map((w) => (
              <option key={w} value={w}>
                {w}
              </option>
            ))}
          </NativeSelect>
        </div>
        <Button type="submit" variant="outline">
          {t("rum.funnel.apply")}
        </Button>
      </form>
      <p className="text-xs text-muted-foreground">{t("rum.funnel.hint")}</p>

      {steps.length < 2 ? (
        <p className="rounded-xl border border-dashed p-6 text-center text-sm text-muted-foreground">
          <Filter className="mx-auto mb-2 size-5" aria-hidden="true" />
          {t("rum.funnel.needSteps")}
        </p>
      ) : funnel.isPending ? (
        <LoadingState />
      ) : funnel.isError ? (
        <ErrorState error={funnel.error} onRetry={() => void funnel.refetch()} />
      ) : (
        <>
          <ol className="flex flex-col gap-2" data-testid="rum-funnel">
            {funnel.data.steps.map((s, i) => (
              <Step key={`${i}-${s.step}`} step={s} previous={i === 0 ? undefined : funnel.data.steps[i - 1]} index={i} />
            ))}
          </ol>
          {/* The completion rate is the last step's, and it is the sentence the whole screen exists for. */}
          {funnel.data.steps.length > 0 && (
            <p className="text-sm">
              {t("rum.funnel.completion", {
                share: formatShare(funnel.data.steps[funnel.data.steps.length - 1]!.rate),
                n: formatNumber(funnel.data.steps[0]!.sessions, i18n.language),
                window: funnelWindow,
              })}
            </p>
          )}
        </>
      )}
    </div>
  );
}
