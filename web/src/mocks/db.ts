// MSW handlers for database query performance monitoring (internal/api/dbmon.go, db-monitoring.md §5): one
// PostgreSQL instance of the mock shop with a lock-heavy UPDATE, an index scan and a blocking chain.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { DbInstance, DbQuery, DbSession } from "@/api/db";
import { authenticate } from "./account";

const API = "*/api/v1";

function authed(resolver: HttpResponseResolver): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    if (ctx instanceof Response) return ctx;
    return resolver(info);
  };
}

export const MOCK_DB_INSTANCE = "db1.internal:5432";

const iso = (ms: number) => new Date(ms).toISOString();

const query = (fingerprint: string, text: string, calls: number, total: number, share: number, extra: Partial<DbQuery> = {}): DbQuery => ({
  fingerprint, query_id: fingerprint.slice(0, 8), text, db_names: ["shop"], calls, throughput: calls / 3600, total_time_ms: total,
  avg_ms: total / calls, time_share: share, rows: calls, rows_per_call: 1, rows_examined: 0, errors: 0, no_index_used: 0,
  blocks_hit: calls * 4, blocks_read: calls / 50, cache_hit_ratio: 0.98, ...extra,
});

const QUERIES: DbQuery[] = [
  query("696841490555676800", "UPDATE orders SET amount = amount - ? WHERE id = ?", 1200, 540000, 0.54),
  query("10283524513114126609", "SELECT count(*) FROM orders WHERE email LIKE ?", 42000, 310000, 0.31, { rows_per_call: 1, cache_hit_ratio: 0.71 }),
  query("14652568325837813960", "SELECT * FROM orders WHERE id = ?", 380000, 150000, 0.15),
];

const PLAN = JSON.stringify([{ Plan: { "Node Type": "Index Scan", "Relation Name": "orders", "Index Name": "orders_pkey", "Index Cond": "(id = $1)", "Total Cost": 8.3, "Plan Rows": 1 } }]);

export const dbHandlers = [
  http.get(`${API}/db/instances`, authed(() => {
    const inst: DbInstance = {
      instance: MOCK_DB_INSTANCE, db_system: "postgresql", host_id: "h-db1", host_name: "db1", server_address: "db1.internal", server_port: 5432,
      calls: 423200, throughput: 117.6, total_time_ms: 1000000, avg_ms: 2.36, statements: 3, errors: 0, avg_active_sessions: 3.4, top_wait: "Lock",
      last_seen: iso(Date.now() - 20_000),
    };
    return HttpResponse.json({ instances: [inst] });
  })),
  http.get(`${API}/db/queries`, authed(() => HttpResponse.json({ queries: QUERIES, total_time_ms: 1000000 }))),
  http.get(`${API}/db/queries/:fingerprint`, authed(({ params }) => {
    const q = QUERIES.find((x) => x.fingerprint === params.fingerprint);
    if (!q) return HttpResponse.json({ error: { code: "not_found", message: "statement not found" } }, { status: 404 });
    const to = Date.now();
    const from = to - 3_600_000;
    const points = Array.from({ length: 60 }, (_, i) => ({ t: from + i * 60_000, calls: 20, throughput: 0.33, total_time_ms: 40 + (i % 7), avg_ms: 2 + (i % 7) / 10, rows: 20 }));
    return HttpResponse.json({
      from: iso(from), to: iso(to), step: "60s", db_system: "postgresql", query: q, points,
      plans: [{ plan_hash: "9066c2e27543bd21", format: "json", plan: PLAN, total_cost: 8.3, db_name: "shop", first_seen: iso(from), last_seen: iso(to), captures: 2, is_current: true, plan_change: false }],
      waits: [{ type: "Lock", event: "transactionid", samples: 90, share: 0.9 }, { type: "CPU", event: "", samples: 10, share: 0.1 }],
      callers: [{ service_name: "checkout", environment: "prod", calls: 12000, avg_ms: 3.1, errors: 0 }],
    });
  })),
  http.get(`${API}/db/activity`, authed(() => {
    const to = Date.now();
    const from = to - 3_600_000;
    const pts = (base: number) => Array.from({ length: 60 }, (_, i) => [from + i * 60_000, base + ((i * 7) % 5) / 10]);
    return HttpResponse.json({
      from: iso(from), to: iso(to), step: "60s",
      series: [{ wait_type: "CPU", points: pts(0.8) }, { wait_type: "Lock", points: pts(2.1) }, { wait_type: "IO", points: pts(0.3) }],
      waits: [{ type: "Lock", event: "transactionid", samples: 1445, share: 0.62 }, { type: "CPU", event: "", samples: 560, share: 0.24 }, { type: "IO", event: "DataFileRead", samples: 330, share: 0.14 }],
      top_queries: [{ fingerprint: QUERIES[0]!.fingerprint, text: QUERIES[0]!.text, samples: 1200, avg_active_sessions: 2.1, top_wait: "Lock:transactionid" }],
    });
  })),
  http.get(`${API}/db/sessions`, authed(() => {
    const s = (id: string, extra: Partial<DbSession>): DbSession => ({
      session_id: id, state: "active", wait_type: "Lock", wait_event: "transactionid", db_name: "shop", user: "app", application: "checkout",
      client_address: "10.0.0.7", duration_ms: 4200, fingerprint: QUERIES[0]!.fingerprint, text: QUERIES[0]!.text, blocking_session_ids: [], blocks: 0, ...extra,
    });
    return HttpResponse.json({
      sampled_at: iso(Date.now() - 5_000),
      sessions: [
        s("4700", { state: "idle in transaction", wait_type: "Client", wait_event: "ClientRead", duration_ms: 61000, blocks: 2, fingerprint: "", text: "" }),
        s("4711", { blocking_session_ids: ["4700"] }),
        s("4712", { blocking_session_ids: ["4700"], duration_ms: 3100 }),
        s("4800", { wait_type: "", wait_event: "", duration_ms: 12, fingerprint: QUERIES[2]!.fingerprint, text: QUERIES[2]!.text }),
      ],
    });
  })),
  http.get(`${API}/db/lookup`, authed(({ request }) => {
    const stmt = new URL(request.url).searchParams.get("statement");
    const q = QUERIES.find((x) => x.text === stmt);
    return HttpResponse.json({ matches: q ? [{ instance: MOCK_DB_INSTANCE, fingerprint: q.fingerprint, host_name: "db1", calls: q.calls, avg_ms: q.avg_ms }] : [] });
  })),
];
