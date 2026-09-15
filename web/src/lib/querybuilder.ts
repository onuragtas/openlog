// Query builder model (components/querybuilder): filter conditions of the explorer API (api.md "Fields"), their text
// form (chips, pasted text such as `severity_text IN (ERROR, WARN)`), OR-groups and a compact, validated URL encoding.
import type { FieldType, FilterOp, FilterState, QueryFilter } from "@/api/explorer";

/** API limits (openapi.yaml QueryFilter, FilterGroups). */
export const QB_LIMITS = { conditions: 50, groups: 10, values: 100, valueBytes: 1024, keyLength: 256, q: 1024 } as const;

export const FILTER_OPS = ["=", "!=", "in", "not_in", "contains", "not_contains", "like", "not_like", "regex", "not_regex", "exists", "not_exists", ">", ">=", "<", "<="] as const satisfies readonly FilterOp[];

const COMMON_OPS: FilterOp[] = ["=", "!=", "in", "not_in", "exists", "not_exists"];
const STRING_OPS: FilterOp[] = ["contains", "not_contains", "like", "not_like", "regex", "not_regex"];
const NUMBER_OPS: FilterOp[] = [">", ">=", "<", "<="];

/** Operators offered for a key type (all of them when the type is unknown, e.g. a typed key). */
export function opsForType(type: FieldType | undefined): FilterOp[] {
  if (type === "number") return [...COMMON_OPS, ...NUMBER_OPS];
  if (type === "bool") return COMMON_OPS;
  if (type === "string") return [...COMMON_OPS, ...STRING_OPS];
  return [...COMMON_OPS, ...STRING_OPS, ...NUMBER_OPS];
}

export const isMultiValueOp = (op: FilterOp) => op === "in" || op === "not_in";
export const isNoValueOp = (op: FilterOp) => op === "exists" || op === "not_exists";
export const isNumericOp = (op: FilterOp) => op === ">" || op === ">=" || op === "<" || op === "<=";

/** Text form of an operator (chips and the parser). */
export const OP_TEXT: Record<FilterOp, string> = {
  "=": "=",
  "!=": "!=",
  in: "IN",
  not_in: "NOT IN",
  contains: "CONTAINS",
  not_contains: "NOT CONTAINS",
  like: "LIKE",
  not_like: "NOT LIKE",
  regex: "REGEX",
  not_regex: "NOT REGEX",
  exists: "EXISTS",
  not_exists: "NOT EXISTS",
  ">": ">",
  ">=": ">=",
  "<": "<",
  "<=": "<=",
};

export const emptyFilterState = (): FilterState => ({ filters: [], groups: [], q: "" });

export const conditionCount = (s: Pick<FilterState, "filters" | "groups">) => s.filters.length + s.groups.reduce((n, g) => n + g.length, 0);

export const isFilterStateEmpty = (s: FilterState) => conditionCount(s) === 0 && s.q.trim() === "";

/** Values of a condition as strings (one for single-value operators, none for exists). */
export function filterValues(f: QueryFilter): string[] {
  if (isNoValueOp(f.op)) return [];
  if (isMultiValueOp(f.op)) return (f.values ?? []).map(String);
  return f.value === undefined ? [] : [String(f.value)];
}

/** Builds a condition from text values: numeric operators send numbers when the text is numeric. */
export function makeFilter(key: string, op: FilterOp, values: string[]): QueryFilter {
  if (isNoValueOp(op)) return { key, op };
  if (isMultiValueOp(op)) return { key, op, values };
  const v = values[0] ?? "";
  return { key, op, value: isNumericOp(op) && v.trim() !== "" && Number.isFinite(Number(v)) ? Number(v) : v };
}

const needsQuotes = (v: string) => v === "" || /[\s,()"'=!<>]/.test(v);
const quoteValue = (v: string) => (needsQuotes(v) ? `"${v.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"` : v);
const quoteKey = (k: string) => (/^[^\s=!<>(),"'`~]+$/.test(k) ? k : `\`${k}\``);

/** Text form of a condition, parseable by parseFilterText (e.g. `severity_text IN (ERROR, WARN)`). */
export function formatFilter(f: QueryFilter): string {
  const key = quoteKey(f.key);
  if (isNoValueOp(f.op)) return `${key} ${OP_TEXT[f.op]}`;
  if (isMultiValueOp(f.op)) return `${key} ${OP_TEXT[f.op]} (${filterValues(f).map(quoteValue).join(", ")})`;
  const v = filterValues(f)[0] ?? "";
  return `${key} ${OP_TEXT[f.op]} ${typeof f.value === "number" ? v : quoteValue(v)}`;
}

// ---- parser -------------------------------------------------------------------------------------------------------

/** Words and symbols accepted as operators, longest first within each group. */
const WORD_OPS: [RegExp, FilterOp][] = [
  [/^not[\s_]+in\b/i, "not_in"],
  [/^not[\s_]+contains\b/i, "not_contains"],
  [/^not[\s_]+like\b/i, "not_like"],
  [/^not[\s_]+regex\b/i, "not_regex"],
  [/^not[\s_]+exists\b/i, "not_exists"],
  [/^does\s+not\s+exist\b/i, "not_exists"],
  [/^is\s+not\s+null\b/i, "exists"],
  [/^is\s+null\b/i, "not_exists"],
  [/^in\b/i, "in"],
  [/^contains\b/i, "contains"],
  [/^like\b/i, "like"],
  [/^regex\b/i, "regex"],
  [/^exists\b/i, "exists"],
];
const SYMBOL_OPS: [string, FilterOp][] = [
  ["!=", "!="],
  ["<>", "!="],
  ["==", "="],
  ["=~", "regex"],
  ["!~", "not_regex"],
  [">=", ">="],
  ["<=", "<="],
  ["=", "="],
  [">", ">"],
  ["<", "<"],
];

/** Reads a quoted string starting at s[i] (", ' or `); returns the value and the index after the closing quote. */
function readQuoted(s: string, i: number): { value: string; end: number } | null {
  const q = s[i];
  let out = "";
  for (let j = i + 1; j < s.length; j++) {
    const c = s[j]!;
    if (c === "\\" && q !== "`" && j + 1 < s.length) {
      out += s[++j];
    } else if (c === q) {
      return { value: out, end: j + 1 };
    } else {
      out += c;
    }
  }
  return null;
}

const skipSpace = (s: string, i: number) => {
  while (i < s.length && /\s/.test(s[i]!)) i++;
  return i;
};

/** Splits on top-level ` AND ` (outside quotes and parentheses). */
function splitAnd(s: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s[i]!;
    if (c === '"' || c === "'" || c === "`") {
      const r = readQuoted(s, i);
      if (!r) return [s];
      i = r.end - 1;
    } else if (c === "(") depth++;
    else if (c === ")") depth--;
    else if (depth === 0 && /\s/.test(c) && /^\s+and\s+/i.test(s.slice(i))) {
      parts.push(s.slice(start, i));
      const m = /^\s+and\s+/i.exec(s.slice(i))!;
      i += m[0].length - 1;
      start = i + 1;
    }
  }
  parts.push(s.slice(start));
  return parts.map((p) => p.trim());
}

/** Parses a comma-separated value list, optionally in parentheses. */
function parseList(s: string): string[] | null {
  let body = s.trim();
  if (body.startsWith("(")) {
    if (!body.endsWith(")")) return null;
    body = body.slice(1, -1);
  }
  const values: string[] = [];
  let i = skipSpace(body, 0);
  while (i < body.length) {
    let v: string;
    if (body[i] === '"' || body[i] === "'") {
      const r = readQuoted(body, i);
      if (!r) return null;
      v = r.value;
      i = skipSpace(body, r.end);
    } else {
      const j = body.indexOf(",", i);
      v = (j === -1 ? body.slice(i) : body.slice(i, j)).trim();
      i = j === -1 ? body.length : j;
      if (v === "") return null;
    }
    values.push(v);
    if (i < body.length) {
      if (body[i] !== ",") return null;
      i = skipSpace(body, i + 1);
      if (i >= body.length) return null;
    }
  }
  return values.length > 0 ? values : null;
}

/** Parses one condition such as `http.status_code >= 500`, `k8s.pod.name exists` or `body contains "timeout"`. */
export function parseCondition(text: string): QueryFilter | null {
  const s = text.trim();
  if (!s) return null;
  let i: number;
  let key: string;
  if (s[0] === "`" || s[0] === '"') {
    const r = readQuoted(s, 0);
    if (!r || !r.value) return null;
    key = r.value;
    i = r.end;
  } else {
    const m = /^[^\s=!<>(),"'`~]+/.exec(s);
    if (!m) return null;
    key = m[0];
    i = m[0].length;
  }
  if (key.length > QB_LIMITS.keyLength) return null;
  i = skipSpace(s, i);
  const rest = s.slice(i);
  let op: FilterOp | null = null;
  let after = "";
  const sym = SYMBOL_OPS.find(([t]) => rest.startsWith(t));
  if (sym) {
    op = sym[1];
    after = rest.slice(sym[0].length);
  } else if (i > 0 && /\s/.test(s[i - 1] ?? "")) {
    const word = WORD_OPS.find(([re]) => re.test(rest));
    if (word) {
      op = word[1];
      after = rest.replace(word[0], "");
    }
  }
  if (!op) return null;
  after = after.trim();
  if (isNoValueOp(op)) return after === "" ? { key, op } : null;
  if (isMultiValueOp(op)) {
    const values = parseList(after);
    return values && values.length <= QB_LIMITS.values ? { key, op, values } : null;
  }
  if (after === "") return null;
  let value = after;
  if (after[0] === '"' || after[0] === "'") {
    const r = readQuoted(after, 0);
    if (!r || after.slice(r.end).trim() !== "") return null;
    value = r.value;
  }
  if (new TextEncoder().encode(value).length > QB_LIMITS.valueBytes) return null;
  return makeFilter(key, op, [value]);
}

export type ParsedText = { kind: "filters"; filters: QueryFilter[] } | { kind: "text"; q: string };

/**
 * Parses typed or pasted text: conditions joined with AND become filters; anything that does not parse completely is a
 * free-text body search (surrounding quotes removed).
 */
export function parseFilterText(text: string): ParsedText | null {
  const s = text.trim();
  if (!s) return null;
  const parts = splitAnd(s);
  const filters = parts.map(parseCondition);
  if (filters.every((f): f is QueryFilter => f !== null)) return { kind: "filters", filters };
  const quoted = /^(["'])(.*)\1$/s.exec(s);
  return { kind: "text", q: (quoted ? quoted[2]! : s).slice(0, QB_LIMITS.q) };
}

// ---- groups -------------------------------------------------------------------------------------------------------

/**
 * Canonical form: empty groups dropped; a single remaining group is AND-ed into `filters` (one OR-group equals AND).
 */
export function normalizeFilterState(s: FilterState): FilterState {
  const groups = s.groups.filter((g) => g.length > 0);
  if (groups.length === 1) return { filters: [...s.filters, ...groups[0]!], groups: [], q: s.q };
  return { filters: s.filters, groups, q: s.q };
}

/** Adds conditions as a new OR alternative: current filters become the first group when there are no groups yet. */
export function addOrGroup(s: FilterState, conditions: QueryFilter[]): FilterState {
  if (conditions.length === 0) return s;
  if (s.groups.length === 0) return normalizeFilterState({ filters: [], groups: [s.filters, conditions], q: s.q });
  return normalizeFilterState({ ...s, groups: [...s.groups, conditions] });
}

// ---- URL encoding ---------------------------------------------------------------------------------------------------

type CompactFilter = [string, FilterOp] | [string, FilterOp, string | number | boolean] | [string, FilterOp, (string | number | boolean)[]];

/** Compact URL value: `{f: [[key, op, value?]], g: [[[…]]], q}`; undefined when empty. */
export interface CompactFilterState {
  f?: CompactFilter[];
  g?: CompactFilter[][];
  q?: string;
}

const compact = (f: QueryFilter): CompactFilter => (isNoValueOp(f.op) ? [f.key, f.op] : isMultiValueOp(f.op) ? [f.key, f.op, f.values ?? []] : [f.key, f.op, f.value ?? ""]);

export function encodeFilterState(s: FilterState): CompactFilterState | undefined {
  const n = normalizeFilterState(s);
  const out: CompactFilterState = {};
  if (n.filters.length) out.f = n.filters.map(compact);
  if (n.groups.length) out.g = n.groups.map((g) => g.map(compact));
  if (n.q.trim()) out.q = n.q;
  return Object.keys(out).length ? out : undefined;
}

const scalar = (v: unknown): v is string | number | boolean =>
  (typeof v === "string" && new TextEncoder().encode(v).length <= QB_LIMITS.valueBytes) || (typeof v === "number" && Number.isFinite(v)) || typeof v === "boolean";

/** Validates one condition from untrusted input (URL, saved view): tuple or object form. */
export function sanitizeFilter(raw: unknown): QueryFilter | null {
  let key: unknown, op: unknown, val: unknown;
  if (Array.isArray(raw)) [key, op, val] = raw;
  else if (raw && typeof raw === "object") {
    const o = raw as Record<string, unknown>;
    key = o.key;
    op = o.op;
    val = o.values ?? o.value;
  } else return null;
  if (typeof key !== "string" || key === "" || key.length > QB_LIMITS.keyLength) return null;
  if (typeof op !== "string" || !(FILTER_OPS as readonly string[]).includes(op)) return null;
  const fop = op as FilterOp;
  if (isNoValueOp(fop)) return { key, op: fop };
  if (isMultiValueOp(fop)) {
    const values = Array.isArray(val) ? val : scalar(val) ? [val] : null;
    if (!values || values.length === 0 || values.length > QB_LIMITS.values || !values.every(scalar)) return null;
    return { key, op: fop, values };
  }
  return scalar(val) ? { key, op: fop, value: val } : null;
}

function sanitizeList(raw: unknown): QueryFilter[] {
  return Array.isArray(raw) ? raw.map(sanitizeFilter).filter((f): f is QueryFilter => f !== null) : [];
}

/**
 * Decodes a filter state from untrusted input: a compact object (or its JSON text) or the API shape
 * `{filters, groups, q}`. Invalid conditions are dropped and the API limits are enforced.
 */
export function decodeFilterState(raw: unknown): FilterState {
  let v = raw;
  if (typeof v === "string") {
    try {
      v = JSON.parse(v);
    } catch {
      return emptyFilterState();
    }
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return emptyFilterState();
  const o = v as Record<string, unknown>;
  let budget: number = QB_LIMITS.conditions;
  const take = (list: QueryFilter[]) => {
    const kept = list.slice(0, Math.max(0, budget));
    budget -= kept.length;
    return kept;
  };
  const filters = take(sanitizeList(o.f ?? o.filters));
  const groupsRaw = o.g ?? o.groups;
  const groups = Array.isArray(groupsRaw) ? groupsRaw.slice(0, QB_LIMITS.groups).map((g) => take(sanitizeList(g))) : [];
  const q = typeof o.q === "string" ? o.q.slice(0, QB_LIMITS.q) : "";
  return normalizeFilterState({ filters, groups, q });
}

/** Conditions whose values are suggested for `key`: every condition except those on the key itself (AND-ed filters only). */
export function contextFilters(s: FilterState, key: string): QueryFilter[] {
  return s.filters.filter((f) => f.key !== key);
}
