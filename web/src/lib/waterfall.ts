// Layout math for the trace waterfall. Pure and independent of rendering so a
// canvas/virtualized renderer can reuse it.
import type { Span } from "@/api/types";
import { parseTimestampNs } from "./time";

export interface WaterfallRow {
  span: Span;
  depth: number;
  /** start offset from trace start, ns */
  offsetNs: number;
  /** 0..1 of the trace duration */
  left: number;
  /** 0..1 of the trace duration, at least minWidth */
  width: number;
  childCount: number;
  /** parent span id not present in the trace */
  orphan: boolean;
}

export interface WaterfallLayout {
  rows: WaterfallRow[];
  traceStartNs: bigint;
  totalNs: number;
  services: string[];
}

export function layoutWaterfall(spans: Span[], opts: { minWidth?: number } = {}): WaterfallLayout {
  const minWidth = opts.minWidth ?? 0.002;
  if (spans.length === 0) return { rows: [], traceStartNs: 0n, totalNs: 0, services: [] };

  const starts = new Map<string, bigint>();
  let traceStart: bigint | null = null;
  let traceEnd: bigint | null = null;
  for (const s of spans) {
    const st = parseTimestampNs(s.start) ?? 0n;
    starts.set(s.span_id, st);
    const en = st + BigInt(Math.max(0, Math.round(s.duration_ns)));
    if (traceStart === null || st < traceStart) traceStart = st;
    if (traceEnd === null || en > traceEnd) traceEnd = en;
  }
  const t0 = traceStart!;
  const totalNs = Math.max(1, Number(traceEnd! - t0));

  const ids = new Set(spans.map((s) => s.span_id));
  const children = new Map<string, Span[]>();
  const roots: Span[] = [];
  for (const s of spans) {
    const p = s.parent_span_id;
    if (p && p !== s.span_id && ids.has(p)) {
      const list = children.get(p) ?? [];
      list.push(s);
      children.set(p, list);
    } else {
      roots.push(s);
    }
  }
  const byStart = (a: Span, b: Span) => {
    const d = starts.get(a.span_id)! - starts.get(b.span_id)!;
    return d < 0n ? -1 : d > 0n ? 1 : a.span_id.localeCompare(b.span_id);
  };

  const rows: WaterfallRow[] = [];
  const visited = new Set<string>();
  // Iterative DFS (deep traces must not blow the stack); cycles are guarded by `visited`.
  const stack: { span: Span; depth: number }[] = roots.sort(byStart).reverse().map((span) => ({ span, depth: 0 }));
  while (stack.length > 0) {
    const { span, depth } = stack.pop()!;
    if (visited.has(span.span_id)) continue;
    visited.add(span.span_id);
    const offsetNs = Number(starts.get(span.span_id)! - t0);
    const kids = (children.get(span.span_id) ?? []).sort(byStart);
    const left = offsetNs / totalNs;
    rows.push({
      span,
      depth,
      offsetNs,
      left,
      width: Math.min(Math.max(span.duration_ns / totalNs, minWidth), Math.max(minWidth, 1 - left)),
      childCount: kids.length,
      orphan: !!span.parent_span_id && !ids.has(span.parent_span_id),
    });
    for (let i = kids.length - 1; i >= 0; i--) stack.push({ span: kids[i]!, depth: depth + 1 });
  }
  // Spans only reachable through a cycle: append as roots.
  for (const s of [...spans].sort(byStart)) {
    if (!visited.has(s.span_id)) {
      const offsetNs = Number(starts.get(s.span_id)! - t0);
      rows.push({ span: s, depth: 0, offsetNs, left: offsetNs / totalNs, width: Math.max(s.duration_ns / totalNs, minWidth), childCount: 0, orphan: true });
    }
  }

  const services = [...new Set(spans.map((s) => s.service_name))].sort();
  return { rows, traceStartNs: t0, totalNs, services };
}

/** Evenly spaced tick offsets (ns) for the time axis. */
export function timeTicks(totalNs: number, count = 5): number[] {
  if (totalNs <= 0) return [0];
  return Array.from({ length: count + 1 }, (_, i) => (totalNs * i) / count);
}
