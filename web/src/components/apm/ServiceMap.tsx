// Service map: React Flow canvas (MIT, @xyflow/react) laid out with dagre (lib/apm-map.ts), plus an
// accessible table of the same connections. Service nodes open the service page.
// Phones: when the fitted graph would be unreadably small, the map starts centered on the focused (or most
// connected) service at a readable zoom, with a "Fit all" control and a Map/List toggle.
// A highlighted transaction path dims everything else; dragged node positions persist per user and scope
// (localStorage, `layoutKey`). Large graphs drop edge animation and labels (mapRenderOptions).
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  Panel,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeChange,
  type NodeProps,
  type ReactFlowInstance,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Link } from "@tanstack/react-router";
import { Box, Container, Database, Globe, List, Maximize, MessageSquare, Network, RotateCcw, Server } from "lucide-react";
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ApmMap, ApmMapNode, ApmMapPath } from "@/api/apm";
import { EmptyState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm, parseServiceNodeId } from "@/lib/apm";
import {
  clearLayout,
  CONNECTIONS_CAP,
  elementState,
  initialMapView,
  layoutMap,
  loadLayout,
  mapRenderOptions,
  mergeLayout,
  pathSets,
  saveLayout,
  type ElementState,
  type SavedLayout,
} from "@/lib/apm-map";
import { useIsMobile } from "@/lib/media";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

const NODE_W = 220;
const NODE_H = 88;
const ERROR_EDGE = 0.05;

type NodeData = { node: ApmMapNode; focused: boolean; label: string; locale: string; typeLabel: string; state: ElementState; countsLabel: string };
type MapNode = Node<NodeData, "apm">;

const ICONS = { service: Box, db: Database, external: Globe, messaging: MessageSquare } as const;

const ApmNodeView = memo(function ApmNodeView({ data }: NodeProps<MapNode>) {
  const { node, focused, locale, typeLabel, state, countsLabel } = data;
  const Icon = ICONS[node.type] ?? Box;
  const hasTraffic = node.requests > 0;
  const isService = node.type === "service";
  return (
    <div
      className={cn(
        "flex h-[88px] w-[220px] flex-col justify-between rounded-lg border bg-card px-3 py-2 text-card-foreground shadow-sm transition-opacity",
        isService && "cursor-pointer hover:border-primary",
        focused && "border-primary ring-2 ring-primary/40",
        node.error_rate > ERROR_EDGE && "border-destructive",
        state === "path" && "border-primary ring-2 ring-primary/70",
        state === "dimmed" && "opacity-30",
      )}
      title={data.label}
      data-state={state}
    >
      <Handle type="target" position={Position.Left} className="!size-1.5 !border-0 !bg-muted-foreground" />
      <div className="flex min-w-0 items-center gap-1.5">
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate text-sm font-semibold">{node.name}</span>
      </div>
      <div className="flex min-w-0 items-center gap-2 text-[11px] text-muted-foreground">
        <span className="min-w-0 truncate">{node.environment ? `${typeLabel} · ${node.environment}` : typeLabel}</span>
        {isService && (node.host_count > 0 || node.container_count > 0) && (
          <span className="ml-auto flex shrink-0 items-center gap-1.5 tabular-nums" title={countsLabel}>
            {node.host_count > 0 && (
              <span className="inline-flex items-center gap-0.5">
                <Server className="size-3" aria-hidden="true" />
                {node.host_count}
              </span>
            )}
            {node.container_count > 0 && (
              <span className="inline-flex items-center gap-0.5">
                <Container className="size-3" aria-hidden="true" />
                {node.container_count}
              </span>
            )}
          </span>
        )}
      </div>
      <div className="flex gap-2 font-mono text-[11px] tabular-nums">
        {hasTraffic ? (
          <>
            <span>{formatRpm(node.throughput, locale)}</span>
            <span className={cn(node.error_rate > ERROR_EDGE && "text-destructive-text")}>{formatRate(node.error_rate, locale)}</span>
            <span>{formatMs(node.p95_ms, locale)}</span>
          </>
        ) : (
          <span className="text-muted-foreground">–</span>
        )}
      </div>
      <Handle type="source" position={Position.Right} className="!size-1.5 !border-0 !bg-muted-foreground" />
    </div>
  );
});

const nodeTypes = { apm: ApmNodeView };

export interface ServiceMapProps {
  data: ApmMap;
  /** Node id to highlight (the service page's own service). */
  focusId?: string;
  onOpenService: (service: { name: string; namespace: string; environment: string }) => void;
  height?: number;
  /** Highlighted transaction path (GET /apm/map/path). */
  path?: ApmMapPath | null;
  /** localStorage key of the user's dragged layout (lib/apm-map.ts layoutStorageKey); omitted = not persisted. */
  layoutKey?: string;
}

export function ServiceMap({ data, focusId, onOpenService, height = 520, path, layoutKey }: ServiceMapProps) {
  const { t, i18n } = useTranslation();
  const { resolved } = useTheme();
  const locale = i18n.resolvedLanguage ?? "en";
  const names = useMemo(() => new Map(data.nodes.map((n) => [n.id, n.name])), [data.nodes]);
  const mobile = useIsMobile();
  const flowRef = useRef<ReactFlowInstance<MapNode, Edge> | null>(null);
  const boxRef = useRef<HTMLDivElement>(null);
  const empty = data.nodes.length === 0;
  const [view, setView] = useState<"map" | "list">("map");
  const [centeredOn, setCenteredOn] = useState<string | null>(null);
  const [hoveredEdge, setHoveredEdge] = useState<string | null>(null);
  const [showAllConnections, setShowAllConnections] = useState(false);
  const showList = mobile && view === "list";
  const opts = useMemo(() => mapRenderOptions(data.nodes.length, data.edges.length), [data.nodes.length, data.edges.length]);
  const highlight = useMemo(() => pathSets(path), [path]);

  // Saved layout of this key (re-read when the key changes, e.g. another scope or user).
  const [layout, setLayout] = useState<{ key: string | undefined; positions: SavedLayout }>(() => ({ key: layoutKey, positions: layoutKey ? loadLayout(layoutKey) : {} }));
  if (layout.key !== layoutKey) setLayout({ key: layoutKey, positions: layoutKey ? loadLayout(layoutKey) : {} });
  const hasSavedLayout = Object.keys(layout.positions).length > 0;

  const computed = useMemo(() => layoutMap(data.nodes, data.edges, { nodeWidth: NODE_W, nodeHeight: NODE_H }), [data.nodes, data.edges]);
  const positions = useMemo(() => mergeLayout(computed, layout.positions), [computed, layout.positions]);

  const nodes = useMemo(
    () =>
      data.nodes.map((n): MapNode => {
        const typeLabel = t(`apm.map.nodeTypes.${n.type}`);
        const countsLabel = n.type === "service" ? t("apm.map.nodeCounts", { hosts: n.host_count, containers: n.container_count }) : "";
        const label = t("apm.map.nodeLabel", { type: typeLabel, name: n.name, rpm: formatRpm(n.throughput, locale), errors: formatRate(n.error_rate, locale) });
        return {
          id: n.id,
          type: "apm",
          position: positions.get(n.id) ?? { x: 0, y: 0 },
          data: { node: n, focused: n.id === focusId, locale, typeLabel, label, countsLabel, state: elementState(n.id, highlight?.nodes) },
          ariaLabel: countsLabel ? `${label}, ${countsLabel}` : label,
          draggable: true,
          connectable: false,
        };
      }),
    [data.nodes, positions, focusId, locale, t, highlight],
  );

  const edges = useMemo(
    () =>
      data.edges.map((e): Edge => {
        const bad = e.error_rate > ERROR_EDGE;
        const state = elementState(e.id, highlight?.edges);
        const color = state === "path" ? "var(--primary)" : bad ? "var(--destructive)" : "var(--chart-axis)";
        const showLabel = opts.edgeLabels === "all" || state === "path" || hoveredEdge === e.id;
        return {
          id: e.id,
          source: e.source,
          target: e.target,
          label: showLabel ? `${formatRpm(e.throughput, locale)} · ${formatMs(e.p95_ms, locale)}${bad ? ` · ${formatRate(e.error_rate, locale)}` : ""}` : undefined,
          ariaLabel: t("apm.map.edgeLabel", {
            source: names.get(e.source) ?? e.source,
            target: names.get(e.target) ?? e.target,
            rpm: formatRpm(e.throughput, locale),
            errors: formatRate(e.error_rate, locale),
            p95: formatMs(e.p95_ms, locale),
          }),
          style: { stroke: color, strokeWidth: state === "path" ? 3 : 1.5, opacity: state === "dimmed" ? 0.2 : 1 },
          markerEnd: { type: MarkerType.ArrowClosed, color },
          labelStyle: { fontSize: 11, fill: bad ? "var(--destructive-text)" : "var(--foreground)" },
          labelBgStyle: { fill: "var(--card)" },
          labelBgPadding: [4, 2] as [number, number],
          animated: opts.animateEdges && e.throughput > 0 && state !== "dimmed",
          zIndex: state === "path" ? 1 : 0,
        };
      }),
    [data.edges, highlight, opts, hoveredEdge, locale, names, t],
  );

  const fitAll = useCallback(() => {
    void flowRef.current?.fitView({ padding: 0.15 });
    setCenteredOn(null);
  }, []);

  // Initial viewport: fit, or on a phone center a readable zoom on the focused / most connected node.
  const applyInitialView = useCallback(() => {
    const flow = flowRef.current;
    const el = boxRef.current;
    if (!flow || !el || el.clientWidth === 0) return;
    const v = initialMapView({ positions, edges: data.edges, width: el.clientWidth, height: el.clientHeight, compact: mobile, focusId, nodeWidth: NODE_W, nodeHeight: NODE_H });
    if (v.mode === "fit") {
      fitAll();
    } else {
      void flow.setCenter(v.x, v.y, { zoom: v.zoom });
      setCenteredOn(v.nodeId);
    }
  }, [positions, data.edges, mobile, focusId, fitAll]);

  // Re-apply when the canvas is resized (rotation, drawer, window size, list/map toggle). Positions changed
  // by dragging do not refit (applyInitialView is read through a ref).
  const initialViewRef = useRef(applyInitialView);
  useEffect(() => {
    initialViewRef.current = applyInitialView;
  });
  useEffect(() => {
    const el = boxRef.current;
    if (empty || !el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => initialViewRef.current());
    ro.observe(el);
    return () => ro.disconnect();
  }, [empty]);

  const onNodesChange = useCallback((changes: NodeChange<MapNode>[]) => {
    const moved = changes.filter((c): c is Extract<NodeChange<MapNode>, { type: "position" }> => c.type === "position" && !!c.position);
    if (moved.length === 0) return;
    setLayout((prev) => ({ ...prev, positions: { ...prev.positions, ...Object.fromEntries(moved.map((c) => [c.id, c.position!])) } }));
  }, []);

  const onNodeDragStop = useCallback(
    (_: unknown, _node: MapNode, dragged: MapNode[]) => {
      setLayout((prev) => {
        const next = { ...prev, positions: { ...prev.positions, ...Object.fromEntries(dragged.map((n) => [n.id, n.position])) } };
        if (layoutKey) saveLayout(layoutKey, next.positions);
        return next;
      });
    },
    [layoutKey],
  );

  const resetLayout = () => {
    if (layoutKey) clearLayout(layoutKey);
    setLayout({ key: layoutKey, positions: {} });
    requestAnimationFrame(() => fitAll());
  };

  if (data.nodes.length === 0) return <EmptyState>{t("apm.map.empty")}</EmptyState>;

  const connections = showAllConnections ? data.edges : data.edges.slice(0, CONNECTIONS_CAP);

  return (
    <div className="flex flex-col gap-4">
      {mobile && (
        <div role="group" aria-label={t("apm.map.view")} className="flex gap-2" data-testid="map-view-toggle">
          <Button variant={view === "map" ? "secondary" : "outline"} size="sm" className="min-h-10" aria-pressed={view === "map"} onClick={() => setView("map")}>
            <Network aria-hidden="true" />
            {t("apm.map.viewMap")}
          </Button>
          <Button variant={view === "list" ? "secondary" : "outline"} size="sm" className="min-h-10" aria-pressed={view === "list"} onClick={() => setView("list")}>
            <List aria-hidden="true" />
            {t("apm.map.viewList")}
          </Button>
        </div>
      )}
      <div
        ref={boxRef}
        hidden={showList}
        className="overflow-hidden rounded-lg border bg-background"
        style={{ height: mobile ? Math.min(height, 420) : height }}
        data-testid="service-map"
        data-highlight={highlight ? "path" : undefined}
        role="region"
        aria-label={t("apm.map.canvas")}
      >
        <ReactFlow
          onInit={(instance) => {
            flowRef.current = instance;
            applyInitialView();
          }}
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          colorMode={resolved}
          // Phones pick their initial viewport in applyInitialView (fitting first would flash a tiny graph).
          fitView={!mobile}
          fitViewOptions={{ padding: 0.15 }}
          minZoom={0.1}
          nodesConnectable={false}
          onlyRenderVisibleElements={opts.onlyRenderVisible}
          onNodesChange={onNodesChange}
          onNodeDragStop={onNodeDragStop}
          onEdgeMouseEnter={opts.edgeLabels === "focus" ? (_, e) => setHoveredEdge(e.id) : undefined}
          onEdgeMouseLeave={opts.edgeLabels === "focus" ? () => setHoveredEdge(null) : undefined}
          onNodeClick={(_, n) => {
            const svc = parseServiceNodeId(n.id);
            if (svc) onOpenService(svc);
          }}
        >
          <Background gap={20} />
          <Controls showInteractive={false} />
          <Panel position="top-right" className="flex flex-col items-end gap-1">
            {mobile && (
              <Button variant="outline" size="sm" className="min-h-10 bg-card" onClick={fitAll} data-testid="map-fit-all">
                <Maximize aria-hidden="true" />
                {t("apm.map.fitAll")}
              </Button>
            )}
            {layoutKey && hasSavedLayout && (
              <Button variant="outline" size="sm" className="bg-card" onClick={resetLayout} data-testid="map-reset-layout">
                <RotateCcw aria-hidden="true" />
                {t("apm.map.resetLayout")}
              </Button>
            )}
            {mobile && centeredOn && (
              <span className="rounded bg-card/90 px-1.5 py-0.5 text-[11px] text-muted-foreground" aria-live="polite">
                {t("apm.map.focusedOn", { name: names.get(centeredOn) ?? centeredOn })}
              </span>
            )}
          </Panel>
        </ReactFlow>
      </div>
      <div className="rounded-xl border bg-card">
        <h3 className="px-4 pt-3 text-sm font-semibold">{t("apm.map.connections")}</h3>
        <Table data-testid="map-connections">
          <TableHeader>
            <TableRow>
              <TableHead>{t("apm.map.source")}</TableHead>
              <TableHead>{t("apm.map.target")}</TableHead>
              <TableHead className="text-right">{t("apm.metrics.throughput")}</TableHead>
              <TableHead className="text-right">{t("apm.metrics.errorRate")}</TableHead>
              <TableHead className="text-right">{t("apm.metrics.avg")}</TableHead>
              <TableHead className="text-right">{t("apm.metrics.p95")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {connections.map((e) => (
              <TableRow key={e.id} data-state={highlight?.edges.has(e.id) ? "selected" : undefined}>
                <TableCell>
                  <NodeRef id={e.source} name={names.get(e.source) ?? e.source} />
                </TableCell>
                <TableCell>
                  <NodeRef id={e.target} name={names.get(e.target) ?? e.target} typeLabel={e.target_type === "service" ? undefined : t(`apm.map.nodeTypes.${e.target_type}`)} />
                </TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatRpm(e.throughput, locale)}</TableCell>
                <TableCell className={cn("text-right font-mono tabular-nums", e.error_rate > ERROR_EDGE && "text-destructive-text")}>{formatRate(e.error_rate, locale)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatMs(e.avg_ms, locale)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatMs(e.p95_ms, locale)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {data.edges.length > CONNECTIONS_CAP && (
          <div className="flex justify-center border-t p-2">
            <Button variant="ghost" size="sm" aria-expanded={showAllConnections} onClick={() => setShowAllConnections((s) => !s)}>
              {showAllConnections ? t("apm.map.showFewerConnections") : t("apm.map.showAllConnections", { count: data.edges.length })}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

function NodeRef({ id, name, typeLabel }: { id: string; name: string; typeLabel?: string }) {
  const svc = parseServiceNodeId(id);
  if (!svc) {
    return (
      <span className="font-mono text-xs">
        {name}
        {typeLabel && <span className="ml-1.5 font-sans text-muted-foreground">({typeLabel})</span>}
      </span>
    );
  }
  return (
    <Link
      to="/apm/services/$service"
      params={{ service: svc.name }}
      search={(prev) => ({ range: prev.range, from: prev.from, to: prev.to, ns: svc.namespace || undefined, env: svc.environment || undefined, tab: "map" as const })}
      className="font-medium hover:underline"
    >
      {svc.name}
    </Link>
  );
}
