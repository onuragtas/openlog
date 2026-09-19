// Pure helpers of the database screens (db-monitoring.md §5): session trees, wait colors and plan documents.
import type { DbSession } from "@/api/db";

/** Display name of a db.system.name. */
export function dbSystemName(system: string): string {
  switch (system) {
    case "postgresql":
      return "PostgreSQL";
    case "mysql":
      return "MySQL";
    case "mssql":
      return "SQL Server";
  }
  return system;
}

/**
 * What to call an instance: the server it monitors. The id itself is service.instance.id, which the MySQL
 * integration derives as a hash (semantic-conventions §6.4) — unreadable as a heading, but still the key
 * everything is addressed by, so it stays as the title attribute.
 */
export function instanceLabel(i: { instance: string; server_address: string; server_port: number }): string {
  if (!i.server_address) return i.instance;
  return i.server_port > 0 ? `${i.server_address}:${i.server_port}` : i.server_address;
}

export interface SessionNode {
  session: DbSession;
  children: SessionNode[];
}

/**
 * Blocking forest of one sample: roots are the sessions that block others without being blocked themselves (the
 * heads of the chains), their children the sessions waiting for them. Sessions that neither block nor wait are
 * returned separately. A session waiting for several holders appears under each; a cycle (a deadlock the server
 * has not broken yet) is cut where it closes.
 */
export function blockingForest(sessions: readonly DbSession[]): { roots: SessionNode[]; others: DbSession[] } {
  const byId = new Map(sessions.map((s) => [s.session_id, s]));
  const waiters = new Map<string, DbSession[]>();
  for (const s of sessions) {
    for (const h of s.blocking_session_ids) {
      if (!waiters.has(h)) waiters.set(h, []);
      waiters.get(h)!.push(s);
    }
  }
  const build = (s: DbSession, path: Set<string>): SessionNode => {
    const next = new Set(path).add(s.session_id);
    return { session: s, children: (waiters.get(s.session_id) ?? []).filter((w) => !next.has(w.session_id)).map((w) => build(w, next)) };
  };
  const inChain = new Set<string>();
  for (const s of sessions) if (s.blocking_session_ids.length > 0 || waiters.has(s.session_id)) inChain.add(s.session_id);
  // Heads: in a chain and not waiting for a session of this sample (a holder we did not sample still makes its
  // waiter the visible head).
  let heads = sessions.filter((s) => inChain.has(s.session_id) && !s.blocking_session_ids.some((h) => byId.has(h)));
  if (heads.length === 0) {
    // Only cycles: start each at its member with the most waiters.
    heads = sessions.filter((s) => inChain.has(s.session_id)).sort((a, b) => b.blocks - a.blocks).slice(0, 1);
  }
  const roots = heads.sort((a, b) => b.blocks - a.blocks || b.duration_ms - a.duration_ms).map((s) => build(s, new Set()));
  return { roots, others: sessions.filter((s) => !inChain.has(s.session_id)) };
}

/** Stable colour slot of a wait type across charts (CPU first, then the common classes). */
export const WAIT_ORDER = ["CPU", "Lock", "LWLock", "IO", "io", "lock", "Buffer IO", "IPC", "Client", "Network IO", "Tran Log IO", "Activity", "Timeout", "synch"];

/** Pretty-prints a plan document for display: JSON indented, XML one element per line. */
export function formatPlan(format: string, plan: string): string {
  if (format === "json") {
    try {
      return JSON.stringify(JSON.parse(plan), null, 2);
    } catch {
      return plan;
    }
  }
  let depth = 0;
  return plan
    .replace(/>\s*</g, ">\n<")
    .split("\n")
    .map((line) => {
      if (/^<\//.test(line)) depth = Math.max(depth - 1, 0);
      const out = "  ".repeat(depth) + line;
      // An opening tag alone on its line (not self-closing, not closed on the same line) indents what follows.
      if (/^<[^!?/][^>]*>$/.test(line) && !/\/>$/.test(line) && !/<\/[^>]+>$/.test(line)) depth++;
      return out;
    })
    .join("\n");
}

export interface PlanNode {
  label: string;
  detail: string;
  cost?: number;
  rows?: number;
  children: PlanNode[];
}

/** Tree of a PostgreSQL EXPLAIN (FORMAT JSON) document (null for other formats or invalid documents). */
export function postgresPlanTree(plan: string): PlanNode | null {
  let doc: unknown;
  try {
    doc = JSON.parse(plan);
  } catch {
    return null;
  }
  const root = Array.isArray(doc) ? (doc[0] as { Plan?: unknown } | undefined)?.Plan : undefined;
  if (!root || typeof root !== "object") return null;
  const walk = (n: Record<string, unknown>): PlanNode => {
    const on = [n["Relation Name"], n["Index Name"] ? `using ${n["Index Name"]}` : ""].filter(Boolean).join(" ");
    const cond = [n["Index Cond"], n["Filter"], n["Hash Cond"], n["Join Filter"], n["Recheck Cond"]].filter((x) => typeof x === "string").join(" · ");
    return {
      label: String(n["Node Type"] ?? "?") + (on ? ` on ${on}` : ""),
      detail: cond,
      cost: typeof n["Total Cost"] === "number" ? (n["Total Cost"] as number) : undefined,
      rows: typeof n["Plan Rows"] === "number" ? (n["Plan Rows"] as number) : undefined,
      children: Array.isArray(n["Plans"]) ? (n["Plans"] as Record<string, unknown>[]).map(walk) : [],
    };
  };
  return walk(root as Record<string, unknown>);
}
