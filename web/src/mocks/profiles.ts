// MSW handlers for the continuous profiling endpoints (internal/api/profiles.go, docs/contracts/profiles.md
// §5): one service profiled two ways, so the type selector has a real choice to make — CPU in nanoseconds and
// allocations in bytes. That pairing is the point of the fixture: the two do not add up, which is why the API
// requires a type rather than defaulting to one.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { FlameNode, ProfileFunction, ProfileService } from "@/api/profiles";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1";

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

export const MOCK_PROFILE_SERVICE = "checkout-api";

function services(now: number): ProfileService[] {
  return [
    { service: MOCK_PROFILE_SERVICE, environment: "production", type: "cpu", unit: "nanoseconds", samples: 18420, total: 42_000_000_000, last_seen: formatTs(now - 30_000) },
    { service: MOCK_PROFILE_SERVICE, environment: "production", type: "alloc_space", unit: "bytes", samples: 6210, total: 1_870_000_000, last_seen: formatTs(now - 45_000) },
    { service: "orders-worker", environment: "production", type: "cpu", unit: "nanoseconds", samples: 9310, total: 21_500_000_000, last_seen: formatTs(now - 120_000) },
  ];
}

// The tree the API returns: root named "all", each node holding the total below it.
const CPU_FLAME: FlameNode = {
  name: "all",
  value: 42_000_000_000,
  children: [
    {
      name: "main",
      value: 38_400_000_000,
      children: [
        {
          name: "http.(*Server).Serve",
          value: 31_200_000_000,
          children: [
            {
              name: "checkout.handleOrder",
              value: 24_800_000_000,
              children: [
                { name: "encoding/json.Marshal", value: 12_400_000_000, children: [{ name: "reflect.Value.Interface", value: 5_100_000_000 }] },
                { name: "db.(*Conn).Query", value: 8_900_000_000, children: [{ name: "net.(*conn).Read", value: 7_200_000_000 }] },
                { name: "checkout.priceOrder", value: 3_500_000_000 },
              ],
            },
            { name: "middleware.Auth", value: 6_400_000_000 },
          ],
        },
        { name: "checkout.reconcile", value: 7_200_000_000, children: [{ name: "time.Sleep", value: 6_800_000_000 }] },
      ],
    },
    { name: "runtime.gcBgMarkWorker", value: 3_600_000_000 },
  ],
};

const ALLOC_FLAME: FlameNode = {
  name: "all",
  value: 1_870_000_000,
  children: [
    {
      name: "main",
      value: 1_820_000_000,
      children: [
        { name: "checkout.handleOrder", value: 1_410_000_000, children: [{ name: "encoding/json.Marshal", value: 1_180_000_000 }] },
        { name: "checkout.reconcile", value: 410_000_000 },
      ],
    },
  ],
};

const CPU_FUNCTIONS: ProfileFunction[] = [
  { function: "net.(*conn).Read", self: 7_200_000_000, samples: 3160 },
  { function: "time.Sleep", self: 6_800_000_000, samples: 2980 },
  { function: "middleware.Auth", self: 6_400_000_000, samples: 2810 },
  { function: "reflect.Value.Interface", self: 5_100_000_000, samples: 2240 },
  { function: "runtime.gcBgMarkWorker", self: 3_600_000_000, samples: 1580 },
  { function: "checkout.priceOrder", self: 3_500_000_000, samples: 1530 },
];

const ALLOC_FUNCTIONS: ProfileFunction[] = [
  { function: "encoding/json.Marshal", self: 1_180_000_000, samples: 4120 },
  { function: "checkout.reconcile", self: 410_000_000, samples: 1430 },
];

const unitOf = (type: string) => (type === "alloc_space" ? "bytes" : "nanoseconds");

export const profileHandlers = [
  http.get(`${API}/profiles/services`, authed(() => HttpResponse.json({ services: services(Date.now()) }))),

  http.get(`${API}/profiles/flame`, authed(({ request }) => {
    const q = new URL(request.url).searchParams;
    const type = q.get("type") ?? "";
    if (!q.get("service") || !type) {
      return HttpResponse.json({ error: { code: "bad_request", message: "service and type are required" } }, { status: 400 });
    }
    return HttpResponse.json({ unit: unitOf(type), type, flame: type === "alloc_space" ? ALLOC_FLAME : CPU_FLAME });
  })),

  http.get(`${API}/profiles/functions`, authed(({ request }) => {
    const q = new URL(request.url).searchParams;
    const type = q.get("type") ?? "";
    if (!q.get("service") || !type) {
      return HttpResponse.json({ error: { code: "bad_request", message: "service and type are required" } }, { status: 400 });
    }
    const functions = type === "alloc_space" ? ALLOC_FUNCTIONS : CPU_FUNCTIONS;
    return HttpResponse.json({ unit: unitOf(type), type, total: functions.reduce((n, f) => n + f.self, 0), functions });
  })),
];
