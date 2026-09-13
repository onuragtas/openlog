// Service map layout (left-to-right layered graph, dagre). Pure; the React Flow renderer only
// consumes the positions.
import dagre from "@dagrejs/dagre";

export interface MapLayoutOptions {
  nodeWidth?: number;
  nodeHeight?: number;
  rankdir?: "LR" | "TB";
}

export interface Position {
  x: number;
  y: number;
}

/** Node id with the most connections (ties: first in `nodes`); null for an empty graph. */
export function mostConnected(nodes: { id: string }[], edges: { source: string; target: string }[]): string | null {
  const degree = new Map<string, number>(nodes.map((n) => [n.id, 0]));
  for (const e of edges) {
    if (e.source === e.target) continue;
    if (degree.has(e.source)) degree.set(e.source, degree.get(e.source)! + 1);
    if (degree.has(e.target)) degree.set(e.target, degree.get(e.target)! + 1);
  }
  let best: string | null = null;
  let bestDegree = -1;
  for (const n of nodes) {
    const d = degree.get(n.id)!;
    if (d > bestDegree) [best, bestDegree] = [n.id, d];
  }
  return best;
}

export type InitialMapView = { mode: "fit" } | { mode: "focus"; nodeId: string; x: number; y: number; zoom: number };

/** Zoom below which node labels are unreadable. */
export const MIN_READABLE_ZOOM = 0.6;
/** Zoom used when focusing a node on a narrow screen. */
export const FOCUS_ZOOM = 0.85;

/**
 * Initial viewport of the map: fit everything, unless the canvas is compact (a phone) and fitting would zoom
 * below MIN_READABLE_ZOOM; then center (x, y in flow coordinates) on `focusId` or the most connected node.
 */
export function initialMapView(input: {
  positions: Map<string, Position>;
  edges: { source: string; target: string }[];
  width: number;
  height: number;
  compact: boolean;
  focusId?: string;
  nodeWidth?: number;
  nodeHeight?: number;
  padding?: number;
}): InitialMapView {
  const { positions, width, height } = input;
  const nw = input.nodeWidth ?? 220;
  const nh = input.nodeHeight ?? 88;
  if (!input.compact || positions.size === 0 || !(width > 0) || !(height > 0)) return { mode: "fit" };
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const p of positions.values()) {
    minX = Math.min(minX, p.x);
    minY = Math.min(minY, p.y);
    maxX = Math.max(maxX, p.x + nw);
    maxY = Math.max(maxY, p.y + nh);
  }
  const pad = 1 + 2 * (input.padding ?? 0.15);
  const fitZoom = Math.min(width / ((maxX - minX) * pad), height / ((maxY - minY) * pad));
  if (fitZoom >= MIN_READABLE_ZOOM) return { mode: "fit" };
  const ids = [...positions.keys()].map((id) => ({ id }));
  const nodeId = input.focusId && positions.has(input.focusId) ? input.focusId : mostConnected(ids, input.edges)!;
  const p = positions.get(nodeId)!;
  // Never zoom in past 1, and never below the fitted zoom.
  const zoom = Math.max(fitZoom, Math.min(1, FOCUS_ZOOM, width / (nw * 1.25)));
  return { mode: "focus", nodeId, x: p.x + nw / 2, y: p.y + nh / 2, zoom };
}

/** Top-left positions per node id. Cycles and disconnected nodes are handled by dagre. */
export function layoutMap(nodes: { id: string }[], edges: { source: string; target: string }[], opts: MapLayoutOptions = {}): Map<string, Position> {
  const width = opts.nodeWidth ?? 220;
  const height = opts.nodeHeight ?? 88;
  const g = new dagre.graphlib.Graph({ multigraph: false });
  g.setGraph({ rankdir: opts.rankdir ?? "LR", nodesep: 36, ranksep: 110, marginx: 16, marginy: 16 });
  g.setDefaultEdgeLabel(() => ({}));
  const ids = new Set(nodes.map((n) => n.id));
  for (const n of nodes) g.setNode(n.id, { width, height });
  for (const e of edges) {
    if (ids.has(e.source) && ids.has(e.target) && e.source !== e.target) g.setEdge(e.source, e.target);
  }
  dagre.layout(g);
  const out = new Map<string, Position>();
  for (const n of nodes) {
    const p = g.node(n.id);
    out.set(n.id, { x: (p?.x ?? 0) - width / 2, y: (p?.y ?? 0) - height / 2 });
  }
  return out;
}
