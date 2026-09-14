import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { login } from "@/api/account";
import { logsInfiniteQuery } from "@/api/queries";
import { MOCK_EMAIL, MOCK_PASSWORD } from "@/mocks/account";
import { clearLayout, elementState, layoutMap, layoutStorageKey, loadLayout, mapRenderOptions, mergeLayout, pathSets, saveLayout } from "./apm-map";
import { activityTexts, allSelected, pruneSelection, toggleAll } from "./apm-errors";
import { compareDelta, deploymentMarkers } from "./deployments";

describe("error inbox helpers", () => {
  it("turns audit events into activity sentences", () => {
    const at = "2026-09-14T10:00:00.000000000Z";
    const name = (id: string) => (id === "u1" ? "Ada" : id);
    expect(activityTexts({ action: "apm.error_group.update", actor_email: "a@x", created_at: at, details: { status: { from: "unresolved", to: "resolved" }, resolved_in_version: "1.4.3" } }, name)).toEqual([
      { key: "resolvedInVersion", params: { actor: "a@x", version: "1.4.3" } },
    ]);
    expect(activityTexts({ action: "apm.error_group.update", actor_email: "a@x", created_at: at, details: { assignee_user_id: { from: "", to: "u1" } } }, name)).toEqual([
      { key: "assigned", params: { actor: "a@x", name: "Ada" } },
    ]);
    expect(activityTexts({ action: "apm.error_group.regressed", actor_email: "openlog-apm", created_at: at, details: { version: "1.5.0" } }, name)[0]!.key).toBe("regressedVersion");
  });

  it("keeps the selection consistent with the listed groups", () => {
    const all = toggleAll(new Set(), ["a", "b"]);
    expect(allSelected(all, ["a", "b"])).toBe(true);
    expect([...toggleAll(all, ["a", "b"])]).toEqual([]);
    expect([...pruneSelection(all, ["b", "c"])]).toEqual(["b"]);
  });
});

describe("deployments", () => {
  it("marks deployments inside the chart range and judges before/after deltas", () => {
    const markers = deploymentMarkers([{ t: 1_000, version: "1.4.2" }, { t: 99_000, version: "1.4.3" }], 0, 50_000);
    expect(markers).toHaveLength(1);
    expect(markers[0]!.label).toContain("1.4.2");
    expect(compareDelta("p95_ms", 100, 150).verdict).toBe("bad");
    expect(compareDelta("apdex", 0.8, 0.9).verdict).toBe("good");
    expect(compareDelta("error_rate", 0.02, 0.01).verdict).toBe("good");
    expect(compareDelta("p95_ms", null, 10).delta).toBeNull();
  });
});

describe("service map GA helpers", () => {
  it("highlights a path and dims the rest", () => {
    const sets = pathSets({ nodes: ["service:a||", "db:pg"], edges: ["service:a||->db:pg"] });
    expect(elementState("db:pg", sets!.nodes)).toBe("path");
    expect(elementState("external:x", sets!.nodes)).toBe("dimmed");
    expect(elementState("external:x", pathSets(null)?.nodes)).toBe("normal");
  });

  it("drops animation and labels for large graphs", () => {
    expect(mapRenderOptions(10, 20)).toEqual({ animateEdges: true, edgeLabels: "all", onlyRenderVisible: false });
    expect(mapRenderOptions(250, 400)).toEqual({ animateEdges: false, edgeLabels: "focus", onlyRenderVisible: true });
  });

  it("persists dragged positions per user and scope", () => {
    const key = layoutStorageKey("u1", { environment: "prod" });
    expect(key).not.toBe(layoutStorageKey("u2", { environment: "prod" }));
    expect(saveLayout(key, { "service:a||": { x: 10, y: 20 } })).toBe(true);
    const base = new Map([["service:a||", { x: 0, y: 0 }], ["db:pg", { x: 5, y: 5 }]]);
    const merged = mergeLayout(base, { ...loadLayout(key), gone: { x: 1, y: 1 } });
    expect(merged.get("service:a||")).toEqual({ x: 10, y: 20 });
    expect(merged.has("gone")).toBe(false);
    localStorage.setItem(key, "{broken");
    expect(loadLayout(key)).toEqual({});
    clearLayout(key);
    expect(localStorage.getItem(key)).toBeNull();
  });

  it("lays out a 250-node graph quickly", () => {
    const nodes = Array.from({ length: 250 }, (_, i) => ({ id: `n${i}` }));
    const edges = Array.from({ length: 400 }, (_, i) => ({ source: `n${i % 250}`, target: `n${(i * 7 + 13) % 250}` }));
    const start = performance.now();
    const pos = layoutMap(nodes, edges);
    expect(performance.now() - start).toBeLessThan(5000);
    expect(pos.size).toBe(250);
  });
});

describe("logs cursor paging", () => {
  it("follows next_cursor without repeating rows", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const opts = logsInfiniteQuery({ range: { range: "1h" }, limit: 50 });
    const data = await client.fetchInfiniteQuery({ ...opts, pages: 4 });
    expect(data.pages.length).toBeGreaterThan(1);
    const rows = data.pages.flatMap((p) => p.logs);
    expect(data.pages[0]!.logs).toHaveLength(50);
    const ids = rows.map((l) => `${l.timestamp}|${l.body}|${l.host_id}|${l.span_id}`);
    expect(new Set(ids).size).toBe(ids.length);
    // Pages stay newest first across boundaries.
    for (let i = 1; i < rows.length; i++) expect(Date.parse(rows[i - 1]!.timestamp) >= Date.parse(rows[i]!.timestamp)).toBe(true);
  });
});
