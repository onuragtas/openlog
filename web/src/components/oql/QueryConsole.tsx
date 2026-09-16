import { useQuery } from "@tanstack/react-query";
import { ChartBar, Clock, LayoutDashboard, Lightbulb, Play, Table2, Trash2, Wand2 } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { oqlQuery } from "@/api/oql";
import { PageHeader } from "@/components/AppShell";
import { EmptyState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { hasTimeClause } from "@/lib/oql";
import { tDynamic } from "@/lib/onboarding-key";
import { consoleVisualization } from "@/lib/oql-result";
import { addToHistory, clearHistory, loadHistory } from "@/lib/query-history";
import type { RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";
import { AddToDashboardDialog } from "./AddToDashboardDialog";
import { OqlEditor } from "./OqlEditor";
import { QueryResult } from "./QueryResult";
import { QueryWizard } from "./QueryWizard";

export type ConsoleView = "chart" | "table";

/** Examples with a plain-language title, so the list says what a query answers, not only how it is written. */
const EXAMPLES = [
  { id: "logsBySeverity", query: "SELECT count(*) FROM Log FACET severity TIMESERIES" },
  { id: "errorsByService", query: "SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name" },
  { id: "slowTransactions", query: "SELECT percentile(duration.ms, 50, 95), count(*) FROM Transaction FACET transaction.name LIMIT 5" },
  { id: "cpuByHost", query: "SELECT average(value) FROM Metric WHERE metricName = 'system.cpu.utilization' FACET host.name TIMESERIES AUTO" },
  { id: "durationHistogram", query: "SELECT histogram(duration.ms, 1000, 20) FROM Transaction" },
  { id: "trafficVsYesterday", query: "SELECT count(*) FROM Transaction TIMESERIES COMPARE WITH 1 day ago" },
];

export interface QueryConsoleProps {
  /** Query text from the URL (`q`). */
  query?: string;
  view?: ConsoleView;
  /** Global time picker range (sent only when the query has no SINCE/UNTIL). */
  range: RangeSpec;
  onSearchChange: (patch: { q?: string; view?: ConsoleView }) => void;
}

/** Query console: OQL editor, run with the picker range, chart/table result, history and "add to dashboard". */
export function QueryConsole({ query, view = "chart", range, onSearchChange }: QueryConsoleProps) {
  const { t } = useTranslation();
  const [text, setText] = useState(query ?? "");
  const [run, setRun] = useState<{ query: string; id: number } | null>(() => (query?.trim() ? { query, id: 1 } : null));
  const [history, setHistory] = useState<string[]>(() => loadHistory());
  const [adding, setAdding] = useState(false);
  // Someone arriving without a query starts in the guided builder; a shared link opens the query itself.
  const [wizardOpen, setWizardOpen] = useState(() => !query?.trim());
  const [examplesOpen, setExamplesOpen] = useState(false);

  // Back/forward or a shared link changes `q`: follow it (state adjusted while rendering).
  const [syncedQuery, setSyncedQuery] = useState(query);
  if (query !== syncedQuery) {
    setSyncedQuery(query);
    if (query !== undefined && query.trim() && query !== run?.query) {
      setText(query);
      setRun((r) => ({ query, id: (r?.id ?? 0) + 1 }));
    }
  }

  const execute = (q = text) => {
    if (!q.trim()) return;
    setText(q);
    setRun((r) => ({ query: q, id: (r?.id ?? 0) + 1 }));
    setHistory((h) => addToHistory(q, h));
    onSearchChange({ q });
  };

  const ownRange = run ? hasTimeClause(run.query) : false;
  const result = useQuery({ ...oqlQuery({ query: run?.query ?? "", range: ownRange ? null : range, runId: run?.id }), enabled: !!run });
  const data = result.data;
  const visualization = data ? consoleVisualization(data.kind, view) : "line";
  const textOwnRange = useMemo(() => hasTimeClause(text), [text]);

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title={t("oql.console.title")}
        subtitle={t("oql.console.subtitle")}
        actions={
          <Button variant="outline" disabled={!text.trim()} onClick={() => setAdding(true)}>
            <LayoutDashboard aria-hidden="true" />
            {t("oql.addToDashboard.button")}
          </Button>
        }
      />
      <div className="grid min-w-0 gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <div className="flex min-w-0 flex-col gap-4">
          <section aria-label={t("oql.console.editorSection")} className="flex min-w-0 flex-col gap-2 rounded-xl border bg-card p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" variant={wizardOpen ? "secondary" : "outline"} size="sm" aria-expanded={wizardOpen} onClick={() => setWizardOpen((o) => !o)}>
                <Wand2 aria-hidden="true" />
                {t("oql.wizard.title")}
              </Button>
              <Button type="button" variant={examplesOpen ? "secondary" : "outline"} size="sm" aria-expanded={examplesOpen} onClick={() => setExamplesOpen((o) => !o)}>
                <Lightbulb aria-hidden="true" />
                {t("oql.console.showExamples")}
              </Button>
              <span className="hidden text-xs text-muted-foreground sm:inline">{t("oql.console.advancedHint")}</span>
            </div>
            {wizardOpen && (
              <div className="flex min-w-0 flex-col gap-3 rounded-lg border bg-muted/20 p-3">
                <p className="text-xs text-muted-foreground">{t("oql.wizard.subtitle")}</p>
                <QueryWizard range={range} onRun={(q) => execute(q)} />
              </div>
            )}
            {examplesOpen && (
              <ExampleList
                onPick={(q) => {
                  setExamplesOpen(false);
                  execute(q);
                }}
              />
            )}
            <OqlEditor value={text} onChange={setText} onRun={() => execute()} label={t("oql.console.editorLabel")} placeholder={EXAMPLES[0]!.query} minRows={4} />
            <div className="flex flex-wrap items-center gap-2">
              <Button onClick={() => execute()} disabled={!text.trim() || result.isFetching}>
                <Play aria-hidden="true" />
                {t("oql.console.run")}
              </Button>
              <span className="hidden text-xs text-muted-foreground sm:inline">{t("oql.console.runHint")}</span>
              <span className="text-xs text-muted-foreground" data-testid="range-note">
                {textOwnRange ? t("oql.console.queryRange") : t("oql.console.pickerRange")}
              </span>
              <div role="group" aria-label={t("oql.console.view")} className="ml-auto inline-flex rounded-md border p-0.5">
                {(["chart", "table"] as const).map((v) => (
                  <Button
                    key={v}
                    type="button"
                    size="sm"
                    variant={view === v ? "secondary" : "ghost"}
                    aria-pressed={view === v}
                    onClick={() => onSearchChange({ view: v })}
                  >
                    {v === "chart" ? <ChartBar aria-hidden="true" /> : <Table2 aria-hidden="true" />}
                    {t(`oql.console.views.${v}`)}
                  </Button>
                ))}
              </div>
            </div>
          </section>

          <section aria-label={t("oql.console.resultSection")} className="min-w-0 rounded-xl border bg-card p-3">
            {!run ? (
              <div className="flex flex-col gap-3">
                <p className="text-sm text-muted-foreground">{t("oql.console.empty")}</p>
                <ExampleList onPick={execute} />
              </div>
            ) : (
              <QueryResult
                result={data}
                visualization={visualization}
                title={run.query}
                isLoading={result.isFetching}
                error={result.error}
                onRetry={() => void result.refetch()}
                height={280}
                showMetadata
              />
            )}
          </section>
        </div>

        <aside aria-labelledby="query-history-title" className="flex min-w-0 flex-col gap-2 rounded-xl border bg-card p-3 lg:max-h-[calc(100dvh-10rem)] lg:overflow-y-auto">
          <div className="flex items-center justify-between gap-2">
            <h2 id="query-history-title" className="flex items-center gap-1.5 text-sm font-semibold">
              <Clock className="size-4" aria-hidden="true" />
              {t("oql.console.history")}
            </h2>
            {history.length > 0 && (
              <Button variant="ghost" size="sm" onClick={() => setHistory(clearHistory())}>
                <Trash2 aria-hidden="true" />
                {t("oql.console.clearHistory")}
              </Button>
            )}
          </div>
          {history.length === 0 ? (
            <EmptyState className="py-4">{t("oql.console.noHistory")}</EmptyState>
          ) : (
            <ul className="flex flex-col gap-1" data-testid="query-history">
              {history.map((q) => (
                <li key={q}>
                  <button
                    type="button"
                    title={q}
                    onClick={() => execute(q)}
                    className={cn("line-clamp-3 w-full rounded-md px-2 py-1.5 text-left font-mono text-xs break-words hover:bg-accent pointer-coarse:py-2.5", q === run?.query && "bg-accent")}
                  >
                    {q}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>
      </div>
      <AddToDashboardDialog open={adding} onOpenChange={setAdding} query={text} defaultVisualization={data ? consoleVisualization(data.kind, "chart") : "line"} />
    </div>
  );
}

/** The examples, each with a plain-language title above the query text. */
function ExampleList({ onPick }: { onPick: (query: string) => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-2" data-testid="query-examples">
      <h2 className="text-sm font-semibold">{t("oql.console.examples")}</h2>
      <p className="text-xs text-muted-foreground">{t("oql.console.examplesHint")}</p>
      <ul className="flex flex-col gap-1">
        {EXAMPLES.map((e) => (
          <li key={e.id}>
            <button type="button" onClick={() => onPick(e.query)} className="flex w-full flex-col gap-0.5 rounded-md px-2 py-1.5 text-left hover:bg-accent pointer-coarse:py-2.5">
              <span className="text-sm">{tDynamic(t, `oql.examples.${e.id}`)}</span>
              <span className="font-mono text-xs break-words text-muted-foreground">{e.query}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
