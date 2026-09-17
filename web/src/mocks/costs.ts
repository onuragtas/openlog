// MSW handlers for the cost endpoints (internal/api/cost.go, docs/contracts/api.md "Costs"):
// a priced on-demand host running two services, a spot host, and a host openlog cannot price.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { CostContainer, CostHost, CostPricing, CostService, CostSummary } from "@/api/costs";
import { authenticate } from "./account";
import { HOST_IDS } from "./fixtures";

const API = "*/api/v1";

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

export const COST_PRICING: CostPricing = {
  version: 1,
  updated: "2026-09-17",
  currency: "USD",
  note: "Approximate public list prices. They ignore committed-use discounts, credits, taxes, licences, storage, network egress and support.",
  estimated: true,
};

/** web-1: an on-demand m5.large, half busy, both services attributed. */
const webHost: CostHost = {
  host_id: HOST_IDS.web,
  host_name: "web-1",
  provider: "aws",
  instance_type: "m5.large",
  region: "eu-central-1",
  zone: "eu-central-1a",
  lifecycle: "on-demand",
  vcpus: 4,
  memory_bytes: 16 * 1024 ** 3,
  hours: 24,
  price: { usd_per_hour: 0.10944, source: "table", region_multiplier: 1.14 },
  total: 2.6266,
  services: 1.0506,
  unallocated: 0.2627,
  unattributed: 0.1313,
  idle: 1.182,
  used_share: 0.55,
  idle_share: 0.45,
  oversubscribed: false,
  priced: true,
};

/** db-1: no cloud instance facts, priced per vCPU and GB. */
const dbHost: CostHost = {
  host_id: HOST_IDS.db,
  host_name: "db-1",
  provider: "",
  instance_type: "",
  region: "",
  zone: "",
  lifecycle: "",
  vcpus: 8,
  memory_bytes: 32 * 1024 ** 3,
  hours: 24,
  price: { usd_per_hour: 0.424, source: "fallback", note: "host is not in a known cloud; priced per vCPU and per GB at a generic rate", region_multiplier: 1 },
  total: 10.176,
  services: 0,
  unallocated: 0,
  // A database straight on the machine: real work, no container to attribute it to.
  unattributed: 7.123,
  idle: 3.053,
  used_share: 0.7,
  idle_share: 0.3,
  oversubscribed: false,
  priced: true,
};

/** worker-1: reported no capacity at all, so it has no price and is left out of the total. */
const workerHost: CostHost = {
  host_id: HOST_IDS.worker,
  host_name: "worker-1",
  provider: "",
  instance_type: "",
  region: "",
  zone: "",
  lifecycle: "",
  vcpus: 0,
  memory_bytes: 0,
  hours: 0,
  price: { usd_per_hour: 0, source: "none", note: "no instance type and no capacity known for this host; it has no price", region_multiplier: 1 },
  total: 0,
  services: 0,
  unallocated: 0,
  unattributed: 0,
  idle: 0,
  used_share: 0,
  idle_share: 0,
  oversubscribed: false,
  priced: false,
};

const HOSTS: CostHost[] = [dbHost, webHost, workerHost];

const SERVICES: CostService[] = [
  { service_name: "orders", service_namespace: "", environment: "prod", total: 0.7879, hosts: [HOST_IDS.web], containers: 1 },
  { service_name: "catalog", service_namespace: "", environment: "prod", total: 0.2627, hosts: [HOST_IDS.web], containers: 1 },
];

const CONTAINERS: CostContainer[] = [
  { container_id: "0a".repeat(32), container_name: "shop-orders-1", host_id: HOST_IDS.web, host_name: "web-1", service_name: "orders", total: 0.7879, cpu_share: 0.35, memory_share: 0.25, share: 0.3 },
  { container_id: "0b".repeat(32), container_name: "shop-catalog-1", host_id: HOST_IDS.web, host_name: "web-1", service_name: "catalog", total: 0.2627, cpu_share: 0.1, memory_share: 0.1, share: 0.1 },
  { container_id: "0d".repeat(32), container_name: "shop-redis-1", host_id: HOST_IDS.web, host_name: "web-1", service_name: "", total: 0.2627, cpu_share: 0.1, memory_share: 0.1, share: 0.1 },
];

const SUMMARY: CostSummary = {
  currency: "USD",
  total: 12.8026,
  services: 1.0506,
  unallocated: 0.2627,
  unattributed: 7.2543,
  idle: 4.235,
  idle_share: 0.3308,
  per_hour: 0.2667,
  hosts: 3,
  priced_hosts: 2,
  unpriced_hosts: 1,
  host_hours: 48,
};

/** A day of hourly buckets, so the trend chart has something to draw. */
function trendPoints(now: number, step: number, count: number) {
  const points = [];
  for (let i = count - 1; i >= 0; i--) {
    const t = Math.floor((now - i * step) / step) * step;
    const total = 0.5334;
    points.push({ t, total, idle: total * (0.3 + 0.1 * Math.sin(i / 3)) });
  }
  return points;
}

const PRICE_TABLE = {
  version: 1,
  updated: "2026-09-17",
  currency: "USD",
  note: COST_PRICING.note,
  reference_regions: { aws: "us-east-1", gcp: "us-central1", azure: "eastus" },
  sources: ["https://aws.amazon.com/ec2/pricing/on-demand/"],
  instances: { aws: { "m5.large": { on_demand: 0.096, spot: 0.0336 } } },
  region_multipliers: { aws: { "eu-central-1": 1.14 } },
  fallback: { aws: { vcpu_hour: 0.0335, gb_hour: 0.0045 }, default: { vcpu_hour: 0.035, gb_hour: 0.0045 } },
};

export const costHandlers = [
  http.get(`${API}/costs/summary`, authed(() => HttpResponse.json({ summary: SUMMARY, pricing: COST_PRICING, from: Date.now() - 86_400_000, to: Date.now() }))),

  http.get(`${API}/costs/hosts`, authed(() => HttpResponse.json({ hosts: HOSTS, total: HOSTS.length, summary: SUMMARY, pricing: COST_PRICING }))),

  http.get(`${API}/costs/services`, authed(() => HttpResponse.json({ services: SERVICES, total: SERVICES.length, summary: SUMMARY, pricing: COST_PRICING }))),

  http.get(
    `${API}/costs/containers`,
    authed(({ request }) => {
      const hostId = new URL(request.url).searchParams.get("host_id");
      const list = hostId ? CONTAINERS.filter((c) => c.host_id === hostId) : CONTAINERS;
      return HttpResponse.json({ containers: list, total: list.length, summary: SUMMARY, pricing: COST_PRICING });
    }),
  ),

  http.get(
    `${API}/costs/hosts/:hostId`,
    authed(({ params }) => {
      const host = HOSTS.find((h) => h.host_id === String(params.hostId));
      if (!host) return HttpResponse.json({ error: { code: "not_found", message: "host not found" } }, { status: 404 });
      return HttpResponse.json({
        host,
        services: SERVICES.filter((s) => s.hosts.includes(host.host_id)),
        containers: CONTAINERS.filter((c) => c.host_id === host.host_id),
        pricing: COST_PRICING,
        from: Date.now() - 86_400_000,
        to: Date.now(),
      });
    }),
  ),

  http.get(`${API}/costs/trend`, authed(() => HttpResponse.json({ step: "3600s", points: trendPoints(Date.now(), 3_600_000, 24), pricing: COST_PRICING, from: Date.now() - 86_400_000, to: Date.now() }))),

  http.get(`${API}/costs/prices`, authed(() => HttpResponse.json(PRICE_TABLE))),
];
