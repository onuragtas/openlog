// OQL lexing for the editor (docs/contracts/oql.md §1): syntax highlighting, completion context and small
// structural checks (SINCE/UNTIL detection, event type, alert query restrictions). The server parser stays
// authoritative; this tokenizer only needs to be tolerant and fast. Unit-tested in oql.test.ts.

export const OQL_KEYWORDS = [
  "SELECT", "FROM", "WHERE", "FACET", "SINCE", "UNTIL", "TIMESERIES", "LIMIT", "COMPARE", "WITH", "AGO", "AS", "AND", "OR",
  "NOT", "IN", "LIKE", "IS", "NULL", "TRUE", "FALSE", "NOW", "AUTO",
] as const;

export const OQL_FUNCTIONS = [
  "count", "sum", "average", "avg", "min", "max", "uniqueCount", "median", "latest", "earliest", "percentile", "rate", "filter", "histogram",
] as const;

export const OQL_EVENT_TYPES = ["Log", "Span", "Transaction", "Metric", "Host", "Container"] as const;

const UNITS = ["second", "seconds", "sec", "minute", "minutes", "min", "hour", "hours", "day", "days", "week", "weeks"];

const KEYWORD_SET = new Set<string>(OQL_KEYWORDS);
const FUNCTION_SET = new Set(OQL_FUNCTIONS.map((f) => f.toLowerCase()));
const UNIT_SET = new Set(UNITS);

export type OqlTokenType = "keyword" | "function" | "unit" | "identifier" | "backtick" | "string" | "number" | "variable" | "comment" | "operator" | "punctuation" | "whitespace" | "invalid";

export interface OqlToken {
  type: OqlTokenType;
  /** Offsets (UTF-16) into the text, end exclusive. */
  from: number;
  to: number;
  text: string;
}

const IDENT_START = /[A-Za-z_]/;
const IDENT_CHAR = /[A-Za-z0-9_.]/;

/** Matches one token of `src` at `pos` (never an empty match). */
export function matchToken(src: string, pos: number): { type: OqlTokenType; end: number } {
  const c = src[pos]!;
  const next = src[pos + 1];
  if (/\s/.test(c)) {
    let end = pos + 1;
    while (end < src.length && /\s/.test(src[end]!)) end++;
    return { type: "whitespace", end };
  }
  if ((c === "-" && next === "-") || (c === "/" && next === "/")) {
    const nl = src.indexOf("\n", pos);
    return { type: "comment", end: nl === -1 ? src.length : nl };
  }
  if (c === "'" || c === '"') {
    let end = pos + 1;
    while (end < src.length) {
      const ch = src[end]!;
      if (ch === "\\") {
        end += 2;
        continue;
      }
      if (ch === "\n") break;
      if (ch === c) {
        // '' inside a single-quoted string is one quote
        if (c === "'" && src[end + 1] === "'") {
          end += 2;
          continue;
        }
        return { type: "string", end: end + 1 };
      }
      end++;
    }
    return { type: "string", end: Math.min(end, src.length) };
  }
  if (c === "`") {
    const close = src.indexOf("`", pos + 1);
    const nl = src.indexOf("\n", pos + 1);
    if (close === -1 || (nl !== -1 && nl < close)) return { type: "invalid", end: nl === -1 ? src.length : nl };
    return { type: "backtick", end: close + 1 };
  }
  if (c === "{" && next === "{") {
    const close = src.indexOf("}}", pos + 2);
    const nl = src.indexOf("\n", pos + 2);
    if (close === -1 || (nl !== -1 && nl < close)) return { type: "invalid", end: pos + 2 };
    return { type: "variable", end: close + 2 };
  }
  if (/[0-9]/.test(c) || (c === "." && next !== undefined && /[0-9]/.test(next))) {
    const m = /^(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?/.exec(src.slice(pos));
    return { type: "number", end: pos + (m ? m[0].length : 1) };
  }
  if (IDENT_START.test(c)) {
    let end = pos + 1;
    while (end < src.length && IDENT_CHAR.test(src[end]!)) end++;
    // A trailing dot is not part of the identifier ("x." while typing still lexes x).
    while (end > pos + 1 && src[end - 1] === ".") end--;
    const word = src.slice(pos, end);
    if (KEYWORD_SET.has(word.toUpperCase())) return { type: "keyword", end };
    if (FUNCTION_SET.has(word.toLowerCase())) {
      let k = end;
      while (k < src.length && (src[k] === " " || src[k] === "\t")) k++;
      if (src[k] === "(") return { type: "function", end };
    }
    if (UNIT_SET.has(word.toLowerCase())) return { type: "unit", end };
    return { type: "identifier", end };
  }
  if (/[=!<>]/.test(c)) {
    const two = src.slice(pos, pos + 2);
    if (["!=", "<>", "<=", ">="].includes(two)) return { type: "operator", end: pos + 2 };
    return { type: c === "!" ? "invalid" : "operator", end: pos + 1 };
  }
  if (/[(),*[\]+-]/.test(c)) return { type: /[*+-]/.test(c) ? "operator" : "punctuation", end: pos + 1 };
  return { type: "invalid", end: pos + 1 };
}

/** Lexes the whole query (whitespace and comments included). */
export function lexOql(text: string): OqlToken[] {
  const out: OqlToken[] = [];
  let pos = 0;
  while (pos < text.length) {
    const { type, end } = matchToken(text, pos);
    out.push({ type, from: pos, to: end, text: text.slice(pos, end) });
    pos = end;
  }
  return out;
}

const significant = (t: OqlToken) => t.type !== "whitespace" && t.type !== "comment";
const isKw = (t: OqlToken | undefined, kw: string) => t?.type === "keyword" && t.text.toUpperCase() === kw;

/** True when the query has its own SINCE or UNTIL clause (then the console does not send the picker range). */
export function hasTimeClause(text: string): boolean {
  return lexOql(text).some((t) => isKw(t, "SINCE") || isKw(t, "UNTIL"));
}

/** Event type after FROM (as written), or null. */
export function eventTypeOf(text: string): string | null {
  const toks = lexOql(text).filter(significant);
  const i = toks.findIndex((t) => isKw(t, "FROM"));
  const next = i >= 0 ? toks[i + 1] : undefined;
  return next && next.type === "identifier" ? next.text : null;
}

/** Canonical event type name for a written one (case-insensitive), or null. */
export function canonicalEventType(name: string | null | undefined): (typeof OQL_EVENT_TYPES)[number] | null {
  if (!name) return null;
  return OQL_EVENT_TYPES.find((e) => e.toLowerCase() === name.toLowerCase()) ?? null;
}

/** Variable names ({{name}}) used by the query, in order of first use. */
export function variablesOf(text: string): string[] {
  const names: string[] = [];
  for (const t of lexOql(text)) {
    if (t.type !== "variable") continue;
    const n = t.text.slice(2, -2).trim();
    if (n && !names.includes(n)) names.push(n);
  }
  return names;
}

export type CompletionKind = "eventType" | "attribute" | "function" | "keyword";

export interface CompletionContext {
  kind: CompletionKind;
  /** Start offset of the word being completed. */
  from: number;
  prefix: string;
}

const CLAUSES = ["SELECT", "FROM", "WHERE", "FACET", "SINCE", "UNTIL", "TIMESERIES", "LIMIT", "COMPARE"];

/**
 * What to complete at `pos`: event types right after FROM, functions (and attributes inside parentheses) in the
 * SELECT list, attributes in WHERE/FACET, keywords elsewhere. Null inside strings, comments and variables.
 */
export function completionContext(text: string, pos: number): CompletionContext | null {
  const before = text.slice(0, pos);
  const toks = lexOql(before);
  const last = toks[toks.length - 1];
  if (last && (last.type === "string" || last.type === "comment" || last.type === "backtick" || (last.type === "invalid" && last.text.startsWith("`")))) {
    // An unterminated string/comment reaching the cursor.
    if (last.to === pos && (last.type === "comment" || !/['"`]$/.test(last.text) || last.text.length === 1)) return null;
  }
  const m = /[A-Za-z_][A-Za-z0-9_.]*$/.exec(before);
  const prefix = m ? m[0] : "";
  const from = pos - prefix.length;
  const sig = toks.filter((t) => significant(t) && t.to <= from);
  const prev = sig[sig.length - 1];
  if (isKw(prev, "FROM")) return { kind: "eventType", from, prefix };
  let clause = "";
  let depth = 0;
  for (let i = sig.length - 1; i >= 0; i--) {
    const t = sig[i]!;
    if (t.text === ")") depth++;
    else if (t.text === "(") depth--;
    if (t.type === "keyword" && CLAUSES.includes(t.text.toUpperCase())) {
      clause = t.text.toUpperCase();
      break;
    }
  }
  const insideParens = depth < 0;
  switch (clause) {
    case "SELECT":
      return { kind: insideParens ? "attribute" : "function", from, prefix };
    case "WHERE":
    case "FACET":
      // After a complete predicate value the user likely wants a keyword (AND, FACET, …).
      if (prev && (prev.type === "string" || prev.type === "number" || prev.type === "variable" || prev.text === ")") && !insideParens) return { kind: "keyword", from, prefix };
      if (clause === "WHERE" && prev?.type === "identifier") return { kind: "keyword", from, prefix };
      return { kind: "attribute", from, prefix };
    default:
      return { kind: "keyword", from, prefix };
  }
}

export interface AlertQueryIssue {
  /** i18n key under oql.alertRestrictions */
  key: "columns" | "timeseries" | "since" | "until" | "compare" | "histogram" | "variables";
}

/**
 * Restrictions of `oql` alert rules (alerting.md §2.9): exactly one result column; no TIMESERIES, SINCE, UNTIL,
 * COMPARE WITH, histogram() or variables. Syntax errors are left to the server validation.
 */
export function alertQueryIssues(text: string): AlertQueryIssue[] {
  const toks = lexOql(text).filter(significant);
  const issues: AlertQueryIssue[] = [];
  const add = (key: AlertQueryIssue["key"]) => {
    if (!issues.some((i) => i.key === key)) issues.push({ key });
  };
  for (const t of toks) {
    if (isKw(t, "TIMESERIES")) add("timeseries");
    if (isKw(t, "SINCE")) add("since");
    if (isKw(t, "UNTIL")) add("until");
    if (isKw(t, "COMPARE")) add("compare");
    if (t.type === "function" && t.text.toLowerCase() === "histogram") add("histogram");
    if (t.type === "variable") add("variables");
  }
  const s = toks.findIndex((t) => isKw(t, "SELECT"));
  const f = toks.findIndex((t) => isKw(t, "FROM"));
  if (s >= 0 && f > s) {
    let columns = 1;
    let depth = 0;
    let fnDepthStack: string[] = [];
    for (let i = s + 1; i < f; i++) {
      const t = toks[i]!;
      if (t.text === "(") {
        const prev = toks[i - 1];
        fnDepthStack = [...fnDepthStack, prev?.type === "function" ? prev.text.toLowerCase() : ""];
        depth++;
      } else if (t.text === ")") {
        depth--;
        fnDepthStack = fnDepthStack.slice(0, -1);
      } else if (t.text === ",") {
        if (depth === 0) columns++;
        // percentile(x, 50, 95): each level after the first is a column
        else if (depth === 1 && fnDepthStack[0] === "percentile" && countCommasInCurrentCall(toks, i) >= 2) columns++;
      }
    }
    if (columns !== 1) add("columns");
  }
  return issues;
}

/** Number of commas at depth 1 from the opening parenthesis of the call containing index `i` up to `i` (inclusive). */
function countCommasInCurrentCall(toks: OqlToken[], i: number): number {
  let depth = 0;
  let commas = 0;
  for (let k = i; k >= 0; k--) {
    const t = toks[k]!;
    if (t.text === ")") depth++;
    else if (t.text === "(") {
      if (depth === 0) break;
      depth--;
    } else if (t.text === "," && depth === 0) commas++;
  }
  return commas;
}

const quote = (s: string) => `'${s.replace(/\\/g, "\\\\").replace(/'/g, "''")}'`;

/** OQL for a host metric chart ("add to dashboard"): average value of one host, faceted like the chart. */
export function hostMetricOql(metric: string, hostId: string, groupBy: readonly string[] = []): string {
  const facets = groupBy.map((g) => (g.startsWith("resource.") ? `resource[${quote(g.slice(9))}]` : `attributes[${quote(g)}]`));
  return `SELECT average(value) FROM Metric WHERE metricName = ${quote(metric)} AND host.id = ${quote(hostId)}${facets.length ? ` FACET ${facets.join(", ")}` : ""} TIMESERIES AUTO`;
}

/** 1-based line/column of a UTF-16 offset. */
export function lineColumn(text: string, offset: number): { line: number; column: number } {
  const before = text.slice(0, Math.max(0, Math.min(offset, text.length)));
  const lines = before.split("\n");
  return { line: lines.length, column: lines[lines.length - 1]!.length + 1 };
}

/** UTF-16 offset of a 1-based line/column (clamped to the text). */
export function offsetOf(text: string, line: number, column: number): number {
  const lines = text.split("\n");
  let off = 0;
  for (let i = 0; i < Math.min(line - 1, lines.length - 1); i++) off += lines[i]!.length + 1;
  const len = lines[Math.min(line, lines.length) - 1]?.length ?? 0;
  return off + Math.max(0, Math.min(column - 1, len));
}
