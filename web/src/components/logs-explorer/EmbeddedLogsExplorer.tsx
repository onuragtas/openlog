// Logs Explorer embedded in a detail page tab (host, container, Kubernetes pod; D-122): the page context is a locked
// condition, explorer state lives in the page URL under `l*` parameters, and pre-explorer parameters (`severity`,
// `lsrc`, `lfile`, `ldisc`, `lunit`, `stream`) open as editable chips until the filters change.
import { useNavigate } from "@tanstack/react-router";
import { useMemo, type ReactNode } from "react";
import type { QueryFilter } from "@/api/explorer";
import { legacyTabFilters, type LegacyTabParams } from "@/lib/logs-explorer";
import type { RangeSpec } from "@/lib/time";
import type { EmbeddedLogsSearch } from "@/router";
import { LogsExplorerView, type LogsExplorerParams } from "./LogsExplorerView";

const PARAM_NAMES = { f: "lf", q: "lq", cols: "lcols", order: "lorder", gb: "lgb", tv: "ltv" } as const satisfies Record<keyof LogsExplorerParams, keyof EmbeddedLogsSearch>;

export interface EmbeddedLogsExplorerProps {
  range: RangeSpec;
  search: EmbeddedLogsSearch;
  /** Page context, e.g. `host.id = …` */
  locked: QueryFilter[];
  /** Pre-explorer parameters of the tab and their URL names (cleared once they became chips). */
  legacy: LegacyTabParams;
  legacyParamNames: readonly string[];
  emptyUnfiltered?: ReactNode;
  emptyText?: string;
}

export function EmbeddedLogsExplorer({ range, search, locked, legacy, legacyParamNames, emptyUnfiltered, emptyText }: EmbeddedLogsExplorerProps) {
  const navigate = useNavigate();
  const { severity, source, file, discovery, unit, stream } = legacy;
  const legacyConds = useMemo(() => legacyTabFilters({ severity, source, file, discovery, unit, stream }), [severity, source, file, discovery, unit, stream]);
  const params: LogsExplorerParams = { f: search.lf, q: search.lq, cols: search.lcols, order: search.lorder, gb: search.lgb, tv: search.ltv };
  const update = (patch: Record<string, unknown>, replace = false) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, ...patch }), replace } as never);

  return (
    <LogsExplorerView
      range={range}
      params={params}
      legacy={legacyConds}
      locked={locked}
      emptyUnfiltered={emptyUnfiltered}
      emptyText={emptyText}
      onParams={(patch, opts) => {
        const mapped: Record<string, unknown> = {};
        for (const [k, v] of Object.entries(patch)) mapped[PARAM_NAMES[k as keyof LogsExplorerParams]] = v;
        if (opts.clearLegacy) for (const name of legacyParamNames) mapped[name] = undefined;
        update(mapped, opts.replace);
      }}
      onZoom={(from, to) => update({ range: undefined, from: String(Math.round(from)), to: String(Math.round(to)) })}
    />
  );
}
