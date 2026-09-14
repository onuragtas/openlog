// Dashboard cross-widget filters (api.md "Dashboards" › "Cross-widget filters", oql.md §7): attribute = value pairs
// taken from clicked facet values, kept in the URL search param `filters` and sent with every widget query.
// Unit-tested in dashboard-filters.test.ts.

export interface DashboardFilter {
  attribute: string;
  value: string;
  /** Event type of the widget the value came from (implicit attributes apply only to that event type). */
  event_type?: string;
}

export const MAX_FILTERS = 10;
const MAX_ATTRIBUTE = 256;
const MAX_VALUE = 4096;

// oql.md §1: attribute names, map lookups and backtick-quoted names. The server parses them again.
const ATTRIBUTE = /^(?:[A-Za-z_][A-Za-z0-9_.-]*|`[^`\n]+`|(?:attributes|resource|resource_attributes)\['[^'\n]+'\])$/;
const EVENT_TYPE = /^[A-Za-z]{1,32}$/;
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/;

function clean(v: unknown): DashboardFilter | null {
  if (!v || typeof v !== "object" || Array.isArray(v)) return null;
  const o = v as Record<string, unknown>;
  const attribute = typeof o.attribute === "string" ? o.attribute : typeof o.a === "string" ? o.a : null;
  const raw = o.value ?? o.v;
  const value = typeof raw === "string" ? raw : typeof raw === "number" || typeof raw === "boolean" ? String(raw) : null;
  const et = typeof o.event_type === "string" ? o.event_type : typeof o.e === "string" ? o.e : undefined;
  if (attribute === null || value === null) return null;
  if (attribute.length > MAX_ATTRIBUTE || !ATTRIBUTE.test(attribute) || value.length > MAX_VALUE || CONTROL.test(value)) return null;
  const f: DashboardFilter = { attribute, value };
  if (et && EVENT_TYPE.test(et)) f.event_type = et;
  return f;
}

const same = (a: DashboardFilter, b: DashboardFilter) => a.attribute === b.attribute && a.value === b.value;

/** Sanitizes the untrusted `filters` search param (JSON string or array; at most 10, duplicates dropped). */
export function sanitizeFiltersSearch(v: unknown): DashboardFilter[] | undefined {
  if (typeof v === "string") {
    try {
      return sanitizeFiltersSearch(JSON.parse(v));
    } catch {
      return undefined;
    }
  }
  if (!Array.isArray(v)) return undefined;
  const out: DashboardFilter[] = [];
  for (const item of v) {
    const f = clean(item);
    if (f && !out.some((x) => same(x, f)) && out.length < MAX_FILTERS) out.push(f);
  }
  return out.length > 0 ? out : undefined;
}

/**
 * Adds filters (one per facet of a clicked row) or, when all of them are already set, removes them (toggle).
 * Returns undefined for an empty list (removes the URL param).
 */
export function toggleFilters(current: readonly DashboardFilter[] | undefined, added: readonly DashboardFilter[]): DashboardFilter[] | undefined {
  const list = [...(current ?? [])];
  const valid = added.map(clean).filter((f): f is DashboardFilter => f !== null);
  if (valid.length === 0) return list.length > 0 ? list : undefined;
  const allSet = valid.every((f) => list.some((x) => same(x, f)));
  let next: DashboardFilter[];
  if (allSet) {
    next = list.filter((x) => !valid.some((f) => same(x, f)));
  } else {
    next = list;
    for (const f of valid) {
      if (!next.some((x) => same(x, f))) next.push(f);
    }
    next = next.slice(-MAX_FILTERS);
  }
  return next.length > 0 ? next : undefined;
}

export function removeFilter(current: readonly DashboardFilter[] | undefined, index: number): DashboardFilter[] | undefined {
  const next = (current ?? []).filter((_, i) => i !== index);
  return next.length > 0 ? next : undefined;
}

/** Filters for the clicked facet values of a result row: facet names and values pair up; empty facet names are skipped. */
export function filtersForFacets(facetNames: readonly string[], values: readonly string[], eventType?: string): DashboardFilter[] {
  const out: DashboardFilter[] = [];
  facetNames.forEach((attribute, i) => {
    const value = values[i];
    if (!attribute || value === undefined) return;
    out.push(eventType ? { attribute, value, event_type: eventType } : { attribute, value });
  });
  return out;
}

/** True when the widget's result reported every active filter as not applicable. */
export function allFiltersIgnored(filters: readonly DashboardFilter[] | undefined, ignored: readonly string[] | undefined): boolean {
  if (!filters?.length || !ignored?.length) return false;
  return filters.every((f) => ignored.includes(f.attribute));
}
