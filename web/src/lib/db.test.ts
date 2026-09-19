import { describe, expect, it } from "vitest";
import type { DbSession } from "@/api/db";
import { blockingForest, dbSystemName, formatPlan, postgresPlanTree } from "./db";

const s = (id: string, blockedBy: string[] = [], blocks = 0, duration = 0): DbSession => ({
  session_id: id, state: "active", wait_type: "", wait_event: "", db_name: "shop", user: "app", application: "", client_address: "",
  duration_ms: duration, fingerprint: "", text: "", blocking_session_ids: blockedBy, blocks,
});

describe("blockingForest", () => {
  it("puts the head of a chain first with its waiters below", () => {
    const { roots, others } = blockingForest([s("3", ["2"]), s("1", [], 3), s("2", ["1"], 2), s("4", ["1"]), s("9")]);
    expect(roots.map((r) => r.session.session_id)).toEqual(["1"]);
    expect(roots[0]!.children.map((c) => c.session.session_id).sort()).toEqual(["2", "4"]);
    expect(roots[0]!.children.find((c) => c.session.session_id === "2")!.children[0]!.session.session_id).toBe("3");
    expect(others.map((o) => o.session_id)).toEqual(["9"]);
  });

  it("shows a waiter as the head when its holder was not sampled, and cuts cycles", () => {
    expect(blockingForest([s("5", ["77"])]).roots.map((r) => r.session.session_id)).toEqual(["5"]);
    const cycle = blockingForest([s("a", ["b"], 1), s("b", ["a"], 1)]);
    expect(cycle.roots).toHaveLength(1);
    expect(cycle.roots[0]!.children[0]!.children).toHaveLength(0);
  });
});

describe("plans", () => {
  const pg = JSON.stringify([
    { Plan: { "Node Type": "Nested Loop", "Total Cost": 20.5, "Plan Rows": 3, Plans: [{ "Node Type": "Index Scan", "Relation Name": "orders", "Index Name": "orders_pkey", "Index Cond": "(id = $1)", "Total Cost": 8.3, "Plan Rows": 1 }] } },
  ]);

  it("builds the PostgreSQL plan tree", () => {
    const tree = postgresPlanTree(pg)!;
    expect(tree.label).toBe("Nested Loop");
    expect(tree.children[0]).toMatchObject({ label: "Index Scan on orders using orders_pkey", detail: "(id = $1)", cost: 8.3, rows: 1 });
    expect(postgresPlanTree("<xml/>")).toBeNull();
  });

  it("pretty-prints JSON and XML plans", () => {
    expect(formatPlan("json", '{"a":1}')).toBe('{\n  "a": 1\n}');
    expect(formatPlan("xml", "<A><B x=\"1\"/><C>t</C></A>")).toBe('<A>\n  <B x="1"/>\n  <C>t</C>\n</A>');
  });

  it("names database systems", () => {
    expect(dbSystemName("mssql")).toBe("SQL Server");
    expect(dbSystemName("oracle")).toBe("oracle");
  });
});
