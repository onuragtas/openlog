import { describe, expect, it } from "vitest";
import type { Dashboard, DashboardVariable } from "@/api/dashboards";
import {
  applyLayout,
  clampLayout,
  dashboardToInput,
  duplicateWidget,
  layoutChanged,
  newWidget,
  parseImport,
  requestVariables,
  sanitizeVarsSearch,
  selectedValues,
  stackOrder,
  variableOptions,
} from "./dashboards";

const variable = (p: Partial<DashboardVariable>): DashboardVariable => ({ name: "host", label: "Host", type: "list", query: "", values: [], default: [], multi: false, include_all: false, ...p });

function dashboard(): Dashboard {
  const w = (id: string, x: number, y: number) => ({ ...newWidget("line", []), id, title: id, query: "SELECT count(*) FROM Log", layout: { x, y, w: 6, h: 3 } });
  return {
    id: "d1", name: "D", description: "", visibility: "org", version: 4, variables: [], created_by_user_id: null, created_by_email: "", created_at: "", updated_at: "", can_edit: true,
    pages: [{ id: "p1", name: "Page 1", widgets: [w("b", 6, 0), w("c", 0, 3), w("a", 0, 0)] }],
  };
}

describe("dashboards lib", () => {
  it("orders widgets by row then column for the stacked layout", () => {
    expect(stackOrder(dashboard().pages[0]!.widgets).map((w) => w.id)).toEqual(["a", "b", "c"]);
  });

  it("clamps layouts to the 12-column grid", () => {
    expect(clampLayout({ x: 10, y: -1, w: 6, h: 0 })).toEqual({ x: 6, y: 0, w: 6, h: 1 });
    expect(clampLayout({ x: 0, y: 0, w: 20, h: 99 })).toEqual({ x: 0, y: 0, w: 12, h: 50 });
  });

  it("applies grid changes and converts to PUT input keeping ids and version, dropping draft ids", () => {
    const d = dashboard();
    const layout = [{ i: "a", x: 0, y: 0, w: 12, h: 4 }];
    expect(layoutChanged(d.pages[0]!.widgets, layout)).toBe(true);
    expect(layoutChanged(d.pages[0]!.widgets, [{ i: "a", x: 0, y: 0, w: 6, h: 3 }])).toBe(false);
    const next = applyLayout(d, "p1", layout);
    const dup = duplicateWidget(next, "p1", "a", "(copy)");
    const input = dashboardToInput(dup);
    expect(input.version).toBe(4);
    const widgets = input.pages![0]!.widgets;
    expect(widgets.find((w) => w.id === "a")!.layout).toEqual({ x: 0, y: 0, w: 12, h: 4 });
    const copy = widgets[widgets.length - 1]!;
    expect(copy.id).toBeUndefined();
    expect(copy.title).toBe("a (copy)");
    expect(copy.layout.y).toBe(6);
  });

  it("sanitizes vars search values", () => {
    expect(sanitizeVarsSearch({ host: ["web-1", 2], "bad name": ["x"], env: "prod", obj: [{}] })).toEqual({ host: ["web-1", "2"], env: ["prod"], obj: [] });
    expect(sanitizeVarsSearch('{"host":["a"]}')).toEqual({ host: ["a"] });
    expect(sanitizeVarsSearch("nope")).toBeUndefined();
    expect(sanitizeVarsSearch([1])).toBeUndefined();
  });

  it("resolves selected values and request variables (All sends nothing)", () => {
    const multi = variable({ multi: true, include_all: true });
    const single = variable({ name: "env", default: ["prod"] });
    const text = variable({ name: "q", type: "text" });
    expect(selectedValues(multi, undefined)).toEqual(["*"]);
    expect(selectedValues(single, undefined)).toEqual(["prod"]);
    expect(requestVariables([multi, single, text], undefined)).toEqual({ env: "prod" });
    expect(requestVariables([multi, single, text], { host: ["web-1", "db-1"], env: ["staging"], q: [""] })).toEqual({ host: ["web-1", "db-1"], env: "staging" });
    expect(requestVariables([multi], { host: ["*"] })).toEqual({});
    expect(variableOptions([{ facets: ["a"] }, { facets: ["b"] }, { facets: ["a"] }, { facets: [""] }])).toEqual(["a", "b"]);
  });

  it("parses import documents", () => {
    expect(parseImport("{")).toEqual({ ok: false, error: "json" });
    expect(parseImport('{"name":"x"}')).toEqual({ ok: false, error: "format" });
    const ok = parseImport('{"openlog_dashboard":1,"name":"x","pages":[]}');
    expect(ok).toEqual({ ok: true, doc: { openlog_dashboard: 1, name: "x", pages: [], description: "", variables: [] } });
  });
});
