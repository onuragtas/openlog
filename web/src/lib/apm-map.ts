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

// ---- transaction path highlight ----

export interface PathSets {
  nodes: Set<string>;
  edges: Set<string>;
}

/** Node/edge id sets of a highlighted path (GET /apm/map/path); null when nothing is highlighted. */
export function pathSets(path: { nodes: string[]; edges: string[] } | null | undefined): PathSets | null {
  if (!path) return null;
  return { nodes: new Set(path.nodes), edges: new Set(path.edges) };
}

export type ElementState = "normal" | "path" | "dimmed";

/** Rendering state of a node or edge: without a highlighted path everything is normal. */
export function elementState(id: string, ids: Set<string> | null | undefined): ElementState {
  if (!ids) return "normal";
  return ids.has(id) ? "path" : "dimmed";
}

// ---- rendering limits for large graphs ----

export interface MapRenderOptions {
  /** animated (dashed flowing) edges */
  animateEdges: boolean;
  /** "all": every edge has a label; "focus": only hovered edges and edges on the highlighted path */
  edgeLabels: "all" | "focus";
  /** React Flow onlyRenderVisibleElements */
  onlyRenderVisible: boolean;
}

export const ANIMATE_MAX_EDGES = 60;
export const LABEL_MAX_EDGES = 120;
export const VISIBLE_ONLY_MIN_NODES = 100;
/** Rows of the connections table shown before "show all". */
export const CONNECTIONS_CAP = 100;

export function mapRenderOptions(nodeCount: number, edgeCount: number): MapRenderOptions {
  return {
    animateEdges: edgeCount <= ANIMATE_MAX_EDGES,
    edgeLabels: edgeCount <= LABEL_MAX_EDGES ? "all" : "focus",
    onlyRenderVisible: nodeCount > VISIBLE_ONLY_MIN_NODES,
  };
}

// ---- per-user layout persistence ----

export const LAYOUT_STORAGE_PREFIX = "openlog.apm.map.layout.v1";

export type SavedLayout = Record<string, Position>;

/** Storage key of a dragged layout: per user and per map scope (focused node, or environment/namespace filter). */
export function layoutStorageKey(userId: string, scope: { focusId?: string; environment?: string; namespace?: string }): string {
  const scopeKey = scope.focusId ? `focus=${scope.focusId}` : `env=${scope.environment ?? "*"}|ns=${scope.namespace ?? "*"}`;
  return `${LAYOUT_STORAGE_PREFIX}:${userId || "anonymous"}:${scopeKey}`;
}

function storageOf(storage?: Storage | null): Storage | null {
  if (storage !== undefined) return storage;
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

/** Saved positions (invalid or unavailable storage → empty). */
export function loadLayout(key: string, storage?: Storage | null): SavedLayout {
  try {
    const raw = storageOf(storage)?.getItem(key);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out: SavedLayout = {};
    for (const [id, p] of Object.entries(parsed as Record<string, unknown>)) {
      const pos = p as { x?: unknown; y?: unknown } | null;
      if (pos && typeof pos.x === "number" && typeof pos.y === "number" && Number.isFinite(pos.x) && Number.isFinite(pos.y)) out[id] = { x: pos.x, y: pos.y };
    }
    return out;
  } catch {
    return {};
  }
}

/** Stores positions; returns false when storage is unavailable or full. An empty layout removes the key. */
export function saveLayout(key: string, layout: SavedLayout, storage?: Storage | null): boolean {
  try {
    const s = storageOf(storage);
    if (!s) return false;
    if (Object.keys(layout).length === 0) s.removeItem(key);
    else s.setItem(key, JSON.stringify(layout));
    return true;
  } catch {
    return false;
  }
}

export function clearLayout(key: string, storage?: Storage | null): void {
  saveLayout(key, {}, storage);
}

/** Computed positions with saved positions applied for nodes that still exist. */
export function mergeLayout(base: Map<string, Position>, saved: SavedLayout): Map<string, Position> {
  const out = new Map(base);
  for (const [id, p] of Object.entries(saved)) {
    if (out.has(id)) out.set(id, p);
  }
  return out;
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
