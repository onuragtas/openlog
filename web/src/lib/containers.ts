// Container display helpers (docs/contracts/api.md "Containers").
import type { Container, ContainerTimeseries } from "@/api/containers";
import type { ChartSeriesInput } from "./series";

export type ContainerStatusKey = "running" | "paused" | "restarting" | "exited" | "created" | "dead" | "removing" | "unknown" | "unhealthy" | "notReporting";
export type ContainerStatusVariant = "success" | "warning" | "destructive" | "secondary" | "muted";

/**
 * Badge of a container: its Docker state, "unhealthy" for running containers failing their healthcheck, and
 * "notReporting" for containers believed running (or of unknown state) whose data stopped arriving.
 */
export function containerStatus(c: Pick<Container, "state" | "health" | "reporting">): { key: ContainerStatusKey; variant: ContainerStatusVariant } {
  if (!c.reporting && (c.state === "running" || c.state === "" || c.state === "restarting")) return { key: "notReporting", variant: "muted" };
  switch (c.state) {
    case "running":
      return c.health === "unhealthy" ? { key: "unhealthy", variant: "destructive" } : { key: "running", variant: "success" };
    case "paused":
    case "restarting":
    case "removing":
      return { key: c.state, variant: "warning" };
    case "dead":
      return { key: "dead", variant: "destructive" };
    case "exited":
      return { key: "exited", variant: "secondary" };
    case "created":
      return { key: "created", variant: "muted" };
  }
  return { key: "unknown", variant: "muted" };
}

export const shortContainerId = (id: string) => id.slice(0, 12);

export const containerName = (c: Pick<Container, "name" | "container_id">) => c.name || shortContainerId(c.container_id);

/** "openlog-apmdemo/orders:1" (first tag), or the image name alone. */
export const containerImage = (c: Pick<Container, "image_name" | "image_tags">) => (c.image_tags.length > 0 ? `${c.image_name}:${c.image_tags[0]}` : c.image_name);

/** usage / limit (0..1), null when either is unknown. */
export function memoryRatio(usage: number | null | undefined, limit: number | null | undefined): number | null {
  if (usage == null || limit == null || !(limit > 0)) return null;
  return usage / limit;
}

export interface ComposeServiceGroup {
  /** "project/service"; "" for containers without a compose project and service */
  key: string;
  project: string;
  service: string;
  containers: Container[];
  running: number;
  cpu: number | null;
  memory: number | null;
}

/** Groups containers by compose project and service, keeping their order (the API sorts by project, service, name). */
export function groupByComposeService(cs: Container[]): ComposeServiceGroup[] {
  const out: ComposeServiceGroup[] = [];
  const index = new Map<string, ComposeServiceGroup>();
  for (const c of cs) {
    const key = c.compose_project || c.compose_service ? `${c.compose_project}/${c.compose_service}` : "";
    let g = index.get(key);
    if (!g) {
      g = { key, project: c.compose_project, service: c.compose_service, containers: [], running: 0, cpu: null, memory: null };
      index.set(key, g);
      out.push(g);
    }
    g.containers.push(c);
    if (!c.reporting) continue;
    if (c.state === "running") g.running++;
    if (c.cpu_utilization != null) g.cpu = (g.cpu ?? 0) + c.cpu_utilization;
    if (c.memory_usage != null) g.memory = (g.memory ?? 0) + c.memory_usage;
  }
  // Standalone containers last.
  return [...out.filter((g) => g.key !== ""), ...out.filter((g) => g.key === "")];
}

const pts = (p: number[][]) => p.map((x) => [x[0]!, x[1]!] as [number, number]);

/** Chart series of the container detail page; labels are translated by the caller. */
export function containerChartSeries(series: ContainerTimeseries["series"], labels: Record<"usage" | "limit" | "receive" | "transmit" | "read" | "write", string>) {
  const s = (label: string, p: number[][]): ChartSeriesInput => ({ label, points: pts(p) });
  return {
    cpu: [s("cpu", series.cpu_utilization)],
    memory: [s(labels.usage, series.memory_usage), s(labels.limit, series.memory_limit)],
    network: [s(labels.receive, series.network_receive), s(labels.transmit, series.network_transmit)],
    blockio: [s(labels.read, series.blockio_read), s(labels.write, series.blockio_write)],
  };
}
