// Detail side panel of one log record: every field (row fields, attributes, resource attributes, JSON body keys) with
// filter in / filter out, add or remove as column and copy; the whole record as JSON; trace and span links.
//
// Reading logs is reading a sequence, so the panel also steps: the records around this one are one click (or one
// ArrowUp/ArrowDown) away, as they are on the phone, instead of close-pick-open.
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useState } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { FieldType, LogQueryRow, QueryFilter } from "@/api/explorer";
import { FieldRow } from "@/components/explorer/FieldRow";
import { JsonView } from "@/components/JsonView";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime } from "@/lib/format";
import { jsonBody, recordFields, severityBadgeVariant, valueFilter } from "@/lib/logs-explorer";
import { severityLabel } from "@/lib/severity";
import { parseTimeParam } from "@/lib/time";

export interface LogDetailPanelProps {
  row: LogQueryRow | undefined;
  onClose: () => void;
  /** Where this record sits in the list, 1-based, and how many are loaded. */
  position?: { index: number; total: number };
  /** Moves by one record; absent when there is none in that direction. */
  onStep?: (delta: -1 | 1) => void;
  columns: readonly string[];
  onToggleColumn: (key: string) => void;
  onFilter: (filter: QueryFilter) => void;
  keyTypes: ReadonlyMap<string, FieldType>;
  /** false when the condition limit is reached */
  canFilter: boolean;
}

export function LogDetailPanel({ row, onClose, position, onStep, ...props }: LogDetailPanelProps) {
  const { t } = useTranslation();
  const atFirst = !position || position.index <= 1;
  const atLast = !position || position.index >= position.total;

  // ArrowUp/ArrowDown walk the list while the panel has the page's attention. Ignored while something is being
  // typed into, or the field search would jump records instead of moving the caret.
  useEffect(() => {
    if (!row || !onStep) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
      const el = e.target as HTMLElement | null;
      if (el && (el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName))) return;
      if (e.key === "ArrowUp" && !atFirst) onStep(-1);
      if (e.key === "ArrowDown" && !atLast) onStep(1);
      e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [row, onStep, atFirst, atLast]);

  return (
    <Sheet open={!!row} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="right" title={t("logsExplorer.detail.title")} closeLabel={t("common.close")} className="w-full max-w-full sm:w-[40rem] sm:max-w-[92vw]">
        {row && (
          <>
            {position && onStep && (
              <div className="flex shrink-0 items-center gap-2 border-b px-4 py-2 text-xs text-muted-foreground">
                <span aria-live="polite">{t("logsExplorer.detail.position", { index: position.index, total: position.total })}</span>
                <div className="ml-auto flex gap-1">
                  <Button type="button" variant="outline" size="icon" aria-label={t("logsExplorer.detail.prev")} disabled={atFirst} onClick={() => onStep(-1)}>
                    <ChevronUp aria-hidden="true" />
                  </Button>
                  <Button type="button" variant="outline" size="icon" aria-label={t("logsExplorer.detail.next")} disabled={atLast} onClick={() => onStep(1)}>
                    <ChevronDown aria-hidden="true" />
                  </Button>
                </div>
              </div>
            )}
            <DetailBody key={row.id} row={row} {...props} />
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function DetailBody({ row, columns, onToggleColumn, onFilter, keyTypes, canFilter }: Omit<LogDetailPanelProps, "row" | "onClose" | "position" | "onStep"> & { row: LogQueryRow }) {
  const { t, i18n } = useTranslation();
  const id = useId();
  const [search, setSearch] = useState("");
  const fields = recordFields(row);
  const lower = search.trim().toLowerCase();
  const shown = lower ? fields.filter((f) => f.key.toLowerCase().includes(lower) || f.value.toLowerCase().includes(lower)) : fields;
  const severity = severityLabel(row.severity_text, row.severity_number);
  const { fields: _requested, ...record } = row;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-4">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <time dateTime={row.timestamp} className="font-mono text-xs">
          {formatDateTime(parseTimeParam(row.timestamp) ?? 0, i18n.resolvedLanguage ?? "en", true)}
        </time>
        {severity && <Badge variant={severityBadgeVariant(row.severity_number)}>{severity}</Badge>}
        {row.service_name && <span className="text-muted-foreground">{row.service_name}</span>}
        {row.host_name && <span className="text-muted-foreground">{row.host_name}</span>}
      </div>
      <p className="max-h-40 overflow-y-auto rounded-md bg-muted p-2 font-mono text-xs break-all whitespace-pre-wrap">{row.body}</p>
      {row.trace_id && (
        <div className="flex flex-wrap gap-2">
          <Button asChild variant="outline" size="sm">
            <Link to="/traces/$traceId" params={{ traceId: row.trace_id }}>
              {t("logs.openTrace")}
            </Link>
          </Button>
          {row.span_id && (
            <Button asChild variant="outline" size="sm">
              <Link to="/traces/$traceId" params={{ traceId: row.trace_id }} search={{ span: row.span_id }}>
                {t("logs.openSpan")}
              </Link>
            </Button>
          )}
        </div>
      )}
      <Tabs defaultValue="fields">
        <TabsList>
          <TabsTrigger value="fields">{t("logsExplorer.detail.fields")}</TabsTrigger>
          <TabsTrigger value="json">{t("logsExplorer.detail.json")}</TabsTrigger>
        </TabsList>
        <TabsContent value="fields" className="flex flex-col gap-2">
          <label htmlFor={`${id}-search`} className="sr-only">
            {t("logsExplorer.detail.search")}
          </label>
          <Input id={`${id}-search`} type="search" value={search} placeholder={t("logsExplorer.detail.search")} onChange={(e) => setSearch(e.target.value)} />
          <ul aria-label={t("logsExplorer.detail.fields")} className="flex flex-col">
            {shown.map((f) => (
              <FieldRow key={f.key} field={f} column={columns.includes(f.key)} canFilter={canFilter} onToggleColumn={onToggleColumn} onFilter={(exclude) => onFilter(valueFilter(f.key, f.value, exclude, keyTypes.get(f.key)))} />
            ))}
            {shown.length === 0 && <li className="py-2 text-xs text-muted-foreground">{t("logsExplorer.detail.noFields")}</li>}
          </ul>
        </TabsContent>
        <TabsContent value="json">
          <JsonView value={{ ...record, body: jsonBody(row.body) ?? row.body }} label={t("logsExplorer.detail.json")} className="max-h-[60dvh]" />
        </TabsContent>
      </Tabs>
    </div>
  );
}
