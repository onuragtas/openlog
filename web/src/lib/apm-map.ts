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
