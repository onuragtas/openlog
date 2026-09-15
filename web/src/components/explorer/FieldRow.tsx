// One field of an explorer record detail panel (logs, spans): key, source badge, value and actions — filter in / filter
// out, add or remove as column, copy.
import { Check, CircleMinus, CirclePlus, Columns3, Copy } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { FieldSource } from "@/api/explorer";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { isFilterableValue } from "@/lib/logs-explorer";
import { QB_LIMITS } from "@/lib/querybuilder";

export interface DetailField {
  key: string;
  value: string;
  source: FieldSource;
}

const SOURCE_BADGE = { field: "secondary", attribute: "muted", resource: "outline", body: "muted" } as const;

export function FieldRow({ field, column, canFilter, onToggleColumn, onFilter }: { field: DetailField; column: boolean; canFilter: boolean; onToggleColumn: (key: string) => void; onFilter: (exclude: boolean) => void }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(id);
  }, [copied]);
  const tooLong = !isFilterableValue(field.value);
  const filterTitle = tooLong ? t("logsExplorer.detail.valueTooLong") : !canFilter ? t("queryBuilder.limit", { max: QB_LIMITS.conditions }) : undefined;
  const icon = "size-8 pointer-coarse:size-10";
  return (
    <li className="flex items-start gap-2 border-b py-1.5 last:border-0">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-1.5">
          <span className="min-w-0 font-mono text-[11px] break-all text-muted-foreground">{field.key}</span>
          <Badge variant={SOURCE_BADGE[field.source]} className="px-1 py-0 text-[10px]">
            {t(`queryBuilder.sources.${field.source}`)}
          </Badge>
        </div>
        <div className="font-mono text-xs break-all whitespace-pre-wrap">{field.value}</div>
      </div>
      <div className="flex shrink-0 items-center">
        <Button type="button" variant="ghost" size="icon" className={icon} disabled={tooLong || !canFilter} title={filterTitle ?? t("logsExplorer.detail.filterIn", { key: field.key })} aria-label={t("logsExplorer.detail.filterIn", { key: field.key })} onClick={() => onFilter(false)}>
          <CirclePlus aria-hidden="true" />
        </Button>
        <Button type="button" variant="ghost" size="icon" className={icon} disabled={tooLong || !canFilter} title={filterTitle ?? t("logsExplorer.detail.filterOut", { key: field.key })} aria-label={t("logsExplorer.detail.filterOut", { key: field.key })} onClick={() => onFilter(true)}>
          <CircleMinus aria-hidden="true" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className={icon}
          disabled={field.key === "timestamp"}
          aria-pressed={column}
          title={column ? t("logsExplorer.detail.removeColumn", { key: field.key }) : t("logsExplorer.detail.addColumn", { key: field.key })}
          aria-label={column ? t("logsExplorer.detail.removeColumn", { key: field.key }) : t("logsExplorer.detail.addColumn", { key: field.key })}
          onClick={() => onToggleColumn(field.key)}
        >
          <Columns3 aria-hidden="true" className={column ? "text-primary" : undefined} />
        </Button>
        <Button type="button" variant="ghost" size="icon" className={icon} aria-label={copied ? t("logsExplorer.detail.copied") : t("logsExplorer.detail.copy", { key: field.key })} onClick={() => void copyText(field.value).then(setCopied)}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
        </Button>
      </div>
    </li>
  );
}
