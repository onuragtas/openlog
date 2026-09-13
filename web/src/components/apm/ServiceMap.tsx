// Service map: React Flow canvas (MIT, @xyflow/react) laid out with dagre (lib/apm-map.ts), plus an
// accessible table of the same connections. Service nodes open the service page.
import { Background, Controls, Handle, MarkerType, Position, ReactFlow, type Edge, type Node, type NodeProps, type ReactFlowInstance } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Link } from "@tanstack/react-router";
import { Box, Database, Globe, MessageSquare } from "lucide-react";
import { memo, useEffect, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { ApmMap, ApmMapNode } from "@/api/apm";
import { EmptyState } from "@/components/StateViews";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatMs, formatRate, formatRpm, parseServiceNodeId } from "@/lib/apm";
import { layoutMap } from "@/lib/apm-map";
import { useIsMobile } from "@/lib/media";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

const NODE_W = 220;
const NODE_H = 88;
const ERROR_EDGE = 0.05;

type NodeData = { node: ApmMapNode; focused: boolean; label: string; locale: string; typeLabel: string };
type MapNode = Node<NodeData, "apm">;

const ICONS = { service: Box, db: Database, external: Globe, messaging: MessageSquare } as const;

const ApmNodeView = memo(function ApmNodeView({ data }: NodeProps<MapNode>) {
  const { node, focused, locale, typeLabel } = data;
  const Icon = ICONS[node.type] ?? Box;
  const hasTraffic = node.requests > 0;
  return (
    <div
      className={cn(
        "flex h-[88px] w-[220px] flex-col justify-between rounded-lg border bg-card px-3 py-2 text-card-foreground shadow-sm",
        node.type === "service" && "cursor-pointer hover:border-primary",
        focused && "border-primary ring-2 ring-primary/40",
        node.error_rate > ERROR_EDGE && "border-destructive",
      )}
      title={data.label}
    >
      <Handle type="target" position={Position.Left} className="!size-1.5 !border-0 !bg-muted-foreground" />
      <div className="flex min-w-0 items-center gap-1.5">
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate text-sm font-semibold">{node.name}</span>
      </div>
      <div className="text-[11px] text-muted-foreground">{node.environment ? `${typeLabel} · ${node.environment}` : typeLabel}</div>
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
}

export function ServiceMap({ data, focusId, onOpenService, height = 520 }: ServiceMapProps) {
  const { t, i18n } = useTranslation();
  const { resolved } = useTheme();
  const locale = i18n.resolvedLanguage ?? "en";
  const names = useMemo(() => new Map(data.nodes.map((n) => [n.id, n.name])), [data.nodes]);
  const mobile = useIsMobile();
  const flowRef = useRef<ReactFlowInstance<MapNode, Edge> | null>(null);
  const boxRef = useRef<HTMLDivElement>(null);
  const empty = data.nodes.length === 0;

  // Keep the whole graph in view when the canvas is resized (rotation, drawer, window size).
  useEffect(() => {
    const el = boxRef.current;
    if (empty || !el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => void flowRef.current?.fitView({ padding: 0.15 }));
    ro.observe(el);
    return () => ro.disconnect();
  }, [empty]);

  const { nodes, edges } = useMemo(() => {
    const pos = layoutMap(data.nodes, data.edges, { nodeWidth: NODE_W, nodeHeight: NODE_H });
    const nodes: MapNode[] = data.nodes.map((n) => {
      const typeLabel = t(`apm.map.nodeTypes.${n.type}`);
      return {
        id: n.id,
        type: "apm",
        position: pos.get(n.id) ?? { x: 0, y: 0 },
        data: {
          node: n,
          focused: n.id === focusId,
          locale,
          typeLabel,
          label: t("apm.map.nodeLabel", { type: typeLabel, name: n.name, rpm: formatRpm(n.throughput, locale), errors: formatRate(n.error_rate, locale) }),
        },
        ariaLabel: t("apm.map.nodeLabel", { type: typeLabel, name: n.name, rpm: formatRpm(n.throughput, locale), errors: formatRate(n.error_rate, locale) }),
        draggable: true,
        connectable: false,
      };
    });
    const edges: Edge[] = data.edges.map((e) => {
      const bad = e.error_rate > ERROR_EDGE;
      const color = bad ? "var(--destructive)" : "var(--chart-axis)";
      return {
        id: e.id,
        source: e.source,
        target: e.target,
        label: `${formatRpm(e.throughput, locale)} · ${formatMs(e.p95_ms, locale)}`,
        ariaLabel: t("apm.map.edgeLabel", {
          source: names.get(e.source) ?? e.source,
          target: names.get(e.target) ?? e.target,
          rpm: formatRpm(e.throughput, locale),
          errors: formatRate(e.error_rate, locale),
          p95: formatMs(e.p95_ms, locale),
        }),
        style: { stroke: color, strokeWidth: 1.5 },
        markerEnd: { type: MarkerType.ArrowClosed, color },
        labelStyle: { fontSize: 11, fill: bad ? "var(--destructive-text)" : "var(--foreground)" },
        labelBgStyle: { fill: "var(--card)" },
        labelBgPadding: [4, 2] as [number, number],
        animated: e.throughput > 0,
      };
    });
    return { nodes, edges };
  }, [data, focusId, locale, names, t]);

  if (data.nodes.length === 0) return <EmptyState>{t("apm.map.empty")}</EmptyState>;

  return (
    <div className="flex flex-col gap-4">
      <div
        ref={boxRef}
        className="overflow-hidden rounded-lg border bg-background"
        style={{ height: mobile ? Math.min(height, 420) : height }}
        data-testid="service-map"
        role="region"
        aria-label={t("apm.map.canvas")}
      >
        <ReactFlow
          onInit={(instance) => {
            flowRef.current = instance;
          }}
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          colorMode={resolved}
          fitView
          fitViewOptions={{ padding: 0.15 }}
          minZoom={0.2}
          nodesConnectable={false}
          onNodeClick={(_, n) => {
            const svc = parseServiceNodeId(n.id);
            if (svc) onOpenService(svc);
          }}
        >
          <Background gap={20} />
          <Controls showInteractive={false} />
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
            {data.edges.map((e) => (
              <TableRow key={e.id}>
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
