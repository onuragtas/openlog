// What crashed a mobile application (mobile-crash.md §6). A crash is an error span, so resolving and assigning one stays
// in the APM inbox rather than in a second copy of it here. This list is what the inbox cannot say: which errors were
// fatal, on how many sessions, and with what stack.
//
// The server has already applied whatever symbols were uploaded, and says what it lacked for the rest.
import { useQuery } from "@tanstack/react-query";
import { Bug } from "lucide-react";
import { useTranslation } from "react-i18next";
import { rumCrashesQuery } from "@/api/rum";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { formatDateTime, formatNumber } from "@/lib/format";
import { apiTimeMs } from "@/lib/rum";
import type { RangeSpec } from "@/lib/time";

export function RumCrashesTab({ range, app, environment }: { range: RangeSpec; app: string; environment?: string }) {
  const { t, i18n } = useTranslation();
  const crashes = useQuery(rumCrashesQuery(range, app, environment));

  if (crashes.isPending) return <LoadingState />;
  if (crashes.isError) return <ErrorState error={crashes.error} onRetry={() => void crashes.refetch()} />;
  if (crashes.data.crashes.length === 0) {
    return <EmptyState icon={<Bug className="size-5" aria-hidden="true" />}>{t("rum.crashes.empty")}</EmptyState>;
  }

  return (
    <ul className="flex flex-col gap-2" data-testid="rum-crashes">
      {crashes.data.crashes.map((c) => {
        const last = apiTimeMs(c.last_seen);
        return (
          <li key={`${c.group_id}:${c.frame ?? ""}`} className="flex flex-col gap-1 rounded-xl border bg-card p-3" data-testid="rum-crash">
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="font-mono text-sm font-medium break-all">{c.type}</span>
              <span className="text-xs text-muted-foreground">{t(`rum.crashes.kinds.${c.kind}`, { defaultValue: c.kind })}</span>
              <span className="ml-auto text-sm font-semibold tabular-nums">{formatNumber(c.count, i18n.language)}</span>
            </div>
            {c.message !== "" && <p className="text-sm break-words">{c.message}</p>}
            <p className="text-xs text-muted-foreground">
              {t("rum.crashes.summary", {
                sessions: formatNumber(c.sessions, i18n.language),
                version: c.last_version || t("rum.releases.noVersion"),
                when: last === null ? "–" : formatDateTime(last, i18n.language),
              })}
            </p>
            {c.frame && <p className="font-mono text-xs break-all">{c.frame}</p>}
            {(c.missing_symbols?.length ?? 0) > 0 && (
              <p className="text-xs text-muted-foreground break-all" data-testid="rum-crash-missing-symbols">
                {t("rum.crashes.missingSymbols", { what: c.missing_symbols?.join(", ") ?? "" })}
              </p>
            )}
            {c.stack !== "" && (
              <details className="text-xs">
                <summary className="cursor-pointer text-muted-foreground">{t("rum.crashes.stack")}</summary>
                <pre className="mt-1 overflow-x-auto rounded-lg bg-muted p-2 font-mono whitespace-pre">{c.stack}</pre>
              </details>
            )}
          </li>
        );
      })}
    </ul>
  );
}
