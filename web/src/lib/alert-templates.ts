// Pure helpers for recommended alert templates (alerting.md §2.8): localized texts, parameter form values ↔ API
// params (ratios are edited as percent), fixed targets (host, instance, service) and the editor handoff.
// Unit-tested in alert-templates.test.ts.
import type { AlertTemplate, AlertTemplateParam, AlertTemplateText } from "@/api/alerts";

export type TemplateLanguage = "en" | "tr";

export function templateLanguage(lng: string | undefined): TemplateLanguage {
  return lng?.startsWith("tr") ? "tr" : "en";
}

export function templateText(t: AlertTemplateText | undefined, lng: string | undefined): string {
  if (!t) return "";
  return t[templateLanguage(lng)] || t.en;
}

/** Target fixed by the page that shows the templates (e.g. an integration panel or a host page). */
export interface TemplateTarget {
  hostId?: string;
  hostName?: string;
  discoveryId?: string;
  instance?: string;
  serviceName?: string;
  environment?: string;
}

const TARGET_KEYS: Record<string, keyof TemplateTarget> = {
  host_id: "hostId",
  host_name: "hostName",
  discovery_id: "discoveryId",
  instance: "instance",
  service_name: "serviceName",
  environment: "environment",
};

/** API params supplied by the target (only keys the template declares, non-empty values). */
export function targetParams(t: Pick<AlertTemplate, "params">, target: TemplateTarget): Record<string, string> {
  const out: Record<string, string> = {};
  for (const p of t.params) {
    const k = TARGET_KEYS[p.key];
    const v = k ? target[k] : undefined;
    if (v) out[p.key] = v;
  }
  return out;
}

/** Parameters the user edits: target parameters fixed by the page and helper keys (host_name, discovery_id) are hidden. */
export function editableParams(t: Pick<AlertTemplate, "params">, target: TemplateTarget): AlertTemplateParam[] {
  const fixed = targetParams(t, target);
  return t.params.filter((p) => p.key !== "host_name" && p.key !== "discovery_id" && !(p.key in fixed) && !(p.key === "instance" && !target.instance));
}

/** Whether a template can be used with this target (ratio templates need an instance). */
export function templateUsable(t: Pick<AlertTemplate, "params" | "reference_metric">, target: TemplateTarget): boolean {
  if (t.reference_metric) return !!(target.hostId && target.discoveryId && target.instance);
  return true;
}

const round = (n: number) => Math.round(n * 1e6) / 1e6;

/** Form value of a parameter default (ratio → percent). */
export function initialValue(p: AlertTemplateParam): string {
  const d = p.default;
  if (d === null || d === undefined || d === "") return "";
  if (typeof d === "number") return String(p.unit === "ratio" ? round(d * 100) : d);
  return String(d);
}

export function initialValues(params: AlertTemplateParam[]): Record<string, string> {
  return Object.fromEntries(params.map((p) => [p.key, initialValue(p)]));
}

export type ParamError = { key: "required" } | { key: "number" } | { key: "range"; min: number; max: number };

const isNumeric = (p: AlertTemplateParam) => p.kind === "number" || p.kind === "duration";

/** Displayed range of a numeric parameter (ratio → percent). */
export function displayRange(p: AlertTemplateParam): { min?: number; max?: number } {
  const f = p.unit === "ratio" ? 100 : 1;
  return { min: p.min === undefined ? undefined : round(p.min * f), max: p.max === undefined ? undefined : round(p.max * f) };
}

/** Converts form values to API params and collects client-side errors (the API validates again). */
export function buildParams(
  params: AlertTemplateParam[],
  values: Record<string, string>,
): { params: Record<string, string | number>; errors: Record<string, ParamError> } {
  const out: Record<string, string | number> = {};
  const errors: Record<string, ParamError> = {};
  for (const p of params) {
    const raw = (values[p.key] ?? "").trim();
    if (raw === "") {
      if (p.required) errors[p.key] = { key: "required" };
      continue;
    }
    if (!isNumeric(p)) {
      out[p.key] = raw;
      continue;
    }
    const n = Number(raw.replace(",", "."));
    if (!Number.isFinite(n)) {
      errors[p.key] = { key: "number" };
      continue;
    }
    const r = displayRange(p);
    if ((r.min !== undefined && n < r.min) || (r.max !== undefined && n > r.max)) {
      errors[p.key] = { key: "range", min: r.min ?? -Infinity, max: r.max ?? Infinity };
      continue;
    }
    out[p.key] = p.unit === "ratio" ? round(n / 100) : n;
  }
  return { params: out, errors };
}

export const TEMPLATE_CATEGORIES = ["host", "container", "apm", "integration", "kubernetes"] as const;
export type TemplateCategory = (typeof TEMPLATE_CATEGORIES)[number];

/** Groups templates by category (catalog order kept) and, for integrations, by integration. */
export function groupTemplates(list: AlertTemplate[]): { key: string; category: TemplateCategory; integration?: string; templates: AlertTemplate[] }[] {
  const groups = new Map<string, { key: string; category: TemplateCategory; integration?: string; templates: AlertTemplate[] }>();
  for (const t of list) {
    const key = t.category === "integration" ? `integration:${t.integration ?? ""}` : t.category;
    let g = groups.get(key);
    if (!g) {
      g = { key, category: t.category, integration: t.category === "integration" ? t.integration : undefined, templates: [] };
      groups.set(key, g);
    }
    g.templates.push(t);
  }
  return [...groups.values()];
}
