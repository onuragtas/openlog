// Detail side panel of one span: every field with filter in / out, column and copy actions, the span as JSON, and links
// to the trace waterfall and the span's logs.
import { Link } from "@tanstack/react-router";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { FieldType, QueryFilter, SpanQueryRow } from "@/api/explorer";
import { FieldRow } from "@/components/explorer/FieldRow";
import { JsonView } from "@/components/JsonView";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime } from "@/lib/format";
import { valueFilter } from "@/lib/logs-explorer";
import { parseTimeParam } from "@/lib/time";
import { formatSpanDuration, spanRecordFields } from "@/lib/traces-explorer";
import { SpanStatusBadge } from "./SpansTable";

export interface SpanDetailPanelProps {
  row: SpanQueryRow | undefined;
  onClose: () => void;
  columns: readonly string[];
  onToggleColumn: (key: string) => void;
  onFilter: (filter: QueryFilter) => void;
  keyTypes: ReadonlyMap<string, FieldType>;
  canFilter: boolean;
}

const LOG_MARGIN_MS = 60_000;

export function SpanDetailPanel({ row, onClose, ...props }: SpanDetailPanelProps) {
  const { t } = useTranslation();
  return (
    <Sheet open={!!row} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="right" title={t("tracesExplorer.detail.title")} closeLabel={t("common.close")} className="w-full max-w-full sm:w-[40rem] sm:max-w-[92vw]">
        {row && <DetailBody key={row.id} row={row} {...props} />}
      </SheetContent>
    </Sheet>
  );
}

function DetailBody({ row, columns, onToggleColumn, onFilter, keyTypes, canFilter }: Omit<SpanDetailPanelProps, "row" | "onClose"> & { row: SpanQueryRow }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const id = useId();
  const [search, setSearch] = useState("");
  const fields = spanRecordFields(row);
  const lower = search.trim().toLowerCase();
  const shown = lower ? fields.filter((f) => f.key.toLowerCase().includes(lower) || f.value.toLowerCase().includes(lower)) : fields;
  const start = parseTimeParam(row.timestamp) ?? 0;
  const { fields: _requested, ...record } = row;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-4">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <time dateTime={row.timestamp} className="font-mono text-xs">
          {formatDateTime(start, locale, true)}
        </time>
        <SpanStatusBadge status={row.status_code} />
        {row.service_name && <span className="text-muted-foreground">{row.service_name}</span>}
        <span className="font-mono text-xs tabular-nums">{formatSpanDuration(row.duration_ms, locale)}</span>
      </div>
      <p className="font-medium break-all">{row.name}</p>
      {row.status_message && <p className="rounded-md bg-muted p-2 font-mono text-xs break-all whitespace-pre-wrap">{row.status_message}</p>}
      <div className="flex flex-wrap gap-2">
        <Button asChild variant="outline" size="sm">
          <Link to="/traces/$traceId" params={{ traceId: row.trace_id }} search={{ span: row.span_id }}>
            {t("tracesExplorer.detail.openTrace")}
          </Link>
        </Button>
        <Button asChild variant="outline" size="sm">
          <Link to="/logs" search={{ trace: row.trace_id, span: row.span_id, from: String(Math.floor(start - LOG_MARGIN_MS)), to: String(Math.ceil(start + row.duration_ms + LOG_MARGIN_MS)) }}>
            {t("tracesExplorer.detail.logs")}
          </Link>
        </Button>
      </div>
      <Tabs defaultValue="fields">
        <TabsList>
          <TabsTrigger value="fields">{t("tracesExplorer.detail.fields")}</TabsTrigger>
          <TabsTrigger value="json">{t("tracesExplorer.detail.json")}</TabsTrigger>
        </TabsList>
        <TabsContent value="fields" className="flex flex-col gap-2">
          <label htmlFor={`${id}-search`} className="sr-only">
            {t("tracesExplorer.detail.search")}
          </label>
          <Input id={`${id}-search`} type="search" value={search} placeholder={t("tracesExplorer.detail.search")} onChange={(e) => setSearch(e.target.value)} />
          <ul aria-label={t("tracesExplorer.detail.fields")} className="flex flex-col">
            {shown.map((f) => (
              <FieldRow key={f.key} field={f} column={columns.includes(f.key)} canFilter={canFilter} onToggleColumn={onToggleColumn} onFilter={(exclude) => onFilter(valueFilter(f.key, f.value, exclude, keyTypes.get(f.key)))} />
            ))}
            {shown.length === 0 && <li className="py-2 text-xs text-muted-foreground">{t("tracesExplorer.detail.noFields")}</li>}
          </ul>
        </TabsContent>
        <TabsContent value="json">
          <JsonView value={record} label={t("tracesExplorer.detail.json")} className="max-h-[60dvh]" />
        </TabsContent>
      </Tabs>
    </div>
  );
}
