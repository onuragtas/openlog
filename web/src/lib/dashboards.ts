// Pure dashboard logic: grid ordering and layouts, document ↔ input conversion, variable values (URL ↔ request),
// import parsing and the JSON download. Unit-tested in dashboards.test.ts.
import type {
  Dashboard,
  DashboardImport,
  DashboardInput,
  DashboardVariable,
  DashboardVisualization,
  DashboardWidget,
  DashboardWidgetInput,
  DashboardWidgetLayout,
} from "@/api/dashboards";
import type { OqlVariables } from "@/api/oql";

export const GRID_COLS = 12;
export const ROW_HEIGHT = 80;
export const VISUALIZATIONS: readonly DashboardVisualization[] = ["line", "area", "bar", "table", "billboard", "pie", "heatmap", "markdown"];
export const UNITS = ["", "number", "percent", "bytes", "bytesPerSec", "ms", "s"] as const;
export const VARIABLE_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;
/** Prefix of widget ids that exist only in the browser until the dashboard is saved. */
export const DRAFT_ID_PREFIX = "draft-";

/** Widgets in reading order (row, then column): the stacked single-column layout on narrow screens. */
export function stackOrder<T extends { layout: DashboardWidgetLayout }>(widgets: readonly T[]): T[] {
  return [...widgets].sort((a, b) => a.layout.y - b.layout.y || a.layout.x - b.layout.x);
}

/** First free row below all widgets. */
export function bottomY(widgets: readonly { layout: DashboardWidgetLayout }[]): number {
  return widgets.reduce((m, w) => Math.max(m, w.layout.y + w.layout.h), 0);
}

export function defaultLayout(visualization: DashboardVisualization, widgets: readonly { layout: DashboardWidgetLayout }[]): DashboardWidgetLayout {
  const small = visualization === "billboard";
  return { x: 0, y: bottomY(widgets), w: small ? 4 : 6, h: small ? 2 : 3 };
}

let draftSeq = 0;

export function newWidget(visualization: DashboardVisualization, widgets: readonly { layout: DashboardWidgetLayout }[], patch: Partial<DashboardWidget> = {}): DashboardWidget {
  return {
    id: `${DRAFT_ID_PREFIX}${++draftSeq}`,
    title: "",
    visualization,
    layout: defaultLayout(visualization, widgets),
    query: "",
    markdown: "",
    unit: "",
    thresholds: [],
    options: { legend: true },
    ...patch,
  };
}

/** Clamps a layout to the contract's grid (0 ≤ x, 1 ≤ w ≤ 12, x + w ≤ 12, 0 ≤ y ≤ 10000, 1 ≤ h ≤ 50). */
export function clampLayout(l: DashboardWidgetLayout): DashboardWidgetLayout {
  const w = Math.min(GRID_COLS, Math.max(1, Math.round(l.w)));
  const x = Math.min(GRID_COLS - w, Math.max(0, Math.round(l.x)));
  return { x, y: Math.min(10000, Math.max(0, Math.round(l.y))), w, h: Math.min(50, Math.max(1, Math.round(l.h))) };
}

export function widgetToInput(w: DashboardWidget): DashboardWidgetInput {
  const input: DashboardWidgetInput = {
    title: w.title,
    visualization: w.visualization,
    layout: clampLayout(w.layout),
    query: w.visualization === "markdown" ? "" : w.query,
    markdown: w.visualization === "markdown" ? w.markdown : "",
    unit: w.unit,
    thresholds: w.thresholds,
    options: w.options,
  };
  if (w.id && !w.id.startsWith(DRAFT_ID_PREFIX)) input.id = w.id;
  return input;
}

/** The whole document as PUT input (with the version that was read). */
export function dashboardToInput(d: Dashboard): DashboardInput {
  return {
    name: d.name,
    description: d.description,
    visibility: d.visibility,
    variables: d.variables,
    pages: d.pages.map((p) => ({ id: p.id.startsWith(DRAFT_ID_PREFIX) ? undefined : p.id, name: p.name, widgets: p.widgets.map(widgetToInput) })),
    version: d.version,
  };
}

export interface GridItemLayout {
  i: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

/** True when a grid layout moved or resized any widget. */
export function layoutChanged(widgets: readonly DashboardWidget[], layout: readonly GridItemLayout[]): boolean {
  return layout.some((l) => {
    const w = widgets.find((x) => x.id === l.i);
    return !!w && (w.layout.x !== l.x || w.layout.y !== l.y || w.layout.w !== l.w || w.layout.h !== l.h);
  });
}

/** Applies grid positions to the widgets of one page. */
export function applyLayout(d: Dashboard, pageId: string, layout: readonly GridItemLayout[]): Dashboard {
  const byId = new Map(layout.map((l) => [l.i, l]));
  return {
    ...d,
    pages: d.pages.map((p) =>
      p.id !== pageId
        ? p
        : { ...p, widgets: p.widgets.map((w) => {
            const l = byId.get(w.id);
            return l ? { ...w, layout: clampLayout(l) } : w;
          }) },
    ),
  };
}

/** Replaces (or appends) a widget on a page. */
export function upsertWidget(d: Dashboard, pageId: string, widget: DashboardWidget): Dashboard {
  return {
    ...d,
    pages: d.pages.map((p) => {
      if (p.id !== pageId) return p;
      const exists = p.widgets.some((w) => w.id === widget.id);
      return { ...p, widgets: exists ? p.widgets.map((w) => (w.id === widget.id ? widget : w)) : [...p.widgets, widget] };
    }),
  };
}

export function removeWidget(d: Dashboard, pageId: string, widgetId: string): Dashboard {
  return { ...d, pages: d.pages.map((p) => (p.id === pageId ? { ...p, widgets: p.widgets.filter((w) => w.id !== widgetId) } : p)) };
}

export function duplicateWidget(d: Dashboard, pageId: string, widgetId: string, copySuffix: string): Dashboard {
  const page = d.pages.find((p) => p.id === pageId);
  const w = page?.widgets.find((x) => x.id === widgetId);
  if (!page || !w) return d;
  const copy: DashboardWidget = {
    ...w,
    id: newWidget(w.visualization, []).id,
    title: w.title ? `${w.title} ${copySuffix}` : "",
    layout: { ...w.layout, y: bottomY(page.widgets) },
  };
  return upsertWidget(d, pageId, copy);
}

// ---- variables ----

/** Selected values per variable name (URL search `vars`). */
export type VarValues = Record<string, string[]>;

/** Sanitizes the untrusted `vars` search param. */
export function sanitizeVarsSearch(v: unknown): VarValues | undefined {
  if (typeof v === "string") {
    try {
      return sanitizeVarsSearch(JSON.parse(v));
    } catch {
      return undefined;
    }
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return undefined;
  const out: VarValues = {};
  for (const [name, raw] of Object.entries(v as Record<string, unknown>).slice(0, 10)) {
    if (!VARIABLE_NAME.test(name)) continue;
    const list = (Array.isArray(raw) ? raw : [raw]).filter((x): x is string | number => typeof x === "string" || typeof x === "number").map(String);
    out[name] = list.filter((s) => s.length <= 500).slice(0, 200);
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

/** Values in effect: the URL selection, else the default ("*" = All when the variable offers All and has no default). */
export function selectedValues(variable: DashboardVariable, vars: VarValues | undefined): string[] {
  const fromUrl = vars?.[variable.name];
  if (fromUrl) return fromUrl;
  if (variable.default.length > 0) return variable.default;
  return variable.include_all ? ["*"] : [];
}

export const isAll = (values: readonly string[]) => values.length === 0 || values.includes("*");

/** Request variables for widget queries: "All"/empty sends nothing, single-value variables send a string. */
export function requestVariables(variables: readonly DashboardVariable[], vars: VarValues | undefined): OqlVariables {
  const out: OqlVariables = {};
  for (const v of variables) {
    const values = selectedValues(v, vars).filter((x) => x !== "");
    if (isAll(values)) continue;
    out[v.name] = v.multi ? values : values[0]!;
  }
  return out;
}

/** Locked variable values for share links and reports: the selections in effect, without "All" (name → values). */
export function lockedVariables(variables: readonly DashboardVariable[], vars: VarValues | undefined): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const v of variables) {
    const values = selectedValues(v, vars).filter((x) => x !== "");
    if (isAll(values)) continue;
    out[v.name] = v.multi ? values : values.slice(0, 1);
  }
  return out;
}

/** Values offered by a query variable: the first facet of every result row (deduplicated). */
export function variableOptions(rows: readonly { facets: string[] }[]): string[] {
  const seen = new Set<string>();
  for (const r of rows) {
    const v = r.facets[0];
    if (v !== undefined && v !== "") seen.add(v);
  }
  return [...seen];
}

export function emptyVariable(existing: readonly DashboardVariable[]): DashboardVariable {
  let n = existing.length + 1;
  while (existing.some((v) => v.name === `var${n}`)) n++;
  return { name: `var${n}`, label: "", type: "list", query: "", values: [], default: [], multi: false, include_all: true };
}

// ---- import / export ----

export type ImportParse = { ok: true; doc: DashboardImport } | { ok: false; error: "json" | "format" };

/** Parses a pasted/uploaded export document (the server validates the details). */
export function parseImport(text: string): ImportParse {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return { ok: false, error: "json" };
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return { ok: false, error: "format" };
  const o = v as Record<string, unknown>;
  if (o.openlog_dashboard === undefined || typeof o.name !== "string" || !Array.isArray(o.pages)) return { ok: false, error: "format" };
  return {
    ok: true,
    doc: { ...(o as unknown as DashboardImport), description: typeof o.description === "string" ? o.description : "", variables: Array.isArray(o.variables) ? (o.variables as DashboardVariable[]) : [] },
  };
}

export function exportFileName(name: string): string {
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 60);
  return `${slug || "dashboard"}.openlog-dashboard.json`;
}

/** Saves JSON as a file through a temporary object URL (no-op where object URLs are unavailable). */
export function downloadJson(fileName: string, data: unknown): void {
  if (typeof URL === "undefined" || typeof URL.createObjectURL !== "function") return;
  const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = fileName;
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
