import { describe, expect, it } from "vitest";
import type { InventoryItem } from "@/api/types";
import { hosts, inventory } from "@/mocks/fixtures";
import { countByCategory, filterInventory, summarize } from "./inventory";

const items: InventoryItem[] = [
  { category: "package", key: "dpkg:openssl", data: { manager: "dpkg", name: "openssl", version: "3.0.13" } },
  { category: "package", key: "dpkg:libssl3t64", data: { manager: "dpkg", name: "libssl3t64", version: "3.0.13" } },
  { category: "package", key: "dpkg:curl", data: { manager: "dpkg", name: "curl", version: "8.5.0" } },
  { category: "listening_port", key: "tcp:[::]:22", data: { protocol: "tcp", port: 22, process_name: "sshd" } },
  { category: "custom_thing", key: "x", data: "not json" },
  { category: "os", key: "os", data: { pretty_name: "Ubuntu 24.04 LTS" } },
];

describe("countByCategory", () => {
  it("counts and orders known categories first, then unknown alphabetically", () => {
    expect(countByCategory(items)).toEqual([
      { category: "os", count: 1 },
      { category: "package", count: 3 },
      { category: "listening_port", count: 1 },
      { category: "custom_thing", count: 1 },
    ]);
  });
  it("handles empty", () => expect(countByCategory([])).toEqual([]));
});

describe("filterInventory", () => {
  it("filters by category", () => {
    expect(filterInventory(items, "package", "").map((i) => i.key)).toEqual(["dpkg:openssl", "dpkg:libssl3t64", "dpkg:curl"]);
  });
  it("matches key case-insensitively", () => {
    expect(filterInventory(items, "", "OPENSSL").map((i) => i.key)).toEqual(["dpkg:openssl"]);
  });
  it("matches inside JSON data", () => {
    expect(filterInventory(items, "", "3.0.13").map((i) => i.key)).toEqual(["dpkg:openssl", "dpkg:libssl3t64"]);
    expect(filterInventory(items, "", "sshd").map((i) => i.key)).toEqual(["tcp:[::]:22"]);
  });
  it("requires all whitespace-separated terms", () => {
    expect(filterInventory(items, "", "ssl lib").map((i) => i.key)).toEqual(["dpkg:libssl3t64"]);
    expect(filterInventory(items, "package", "ssl nomatch")).toEqual([]);
  });
  it("matches string data", () => expect(filterInventory(items, "", "not json").map((i) => i.key)).toEqual(["x"]));
  it("combines category and query", () => expect(filterInventory(items, "listening_port", "ssl")).toEqual([]));
  it("scales to realistic fixture sizes", () => {
    const web = hosts(Date.now()).find((h) => h.host_name === "web-1")!;
    const inv = inventory(web);
    expect(inv.length).toBeGreaterThan(500);
    expect(filterInventory(inv, "package", "openssl").map((i) => i.key)).toEqual(["dpkg:openssl"]);
  });
});

describe("summarize", () => {
  it("picks descriptive fields", () => {
    expect(summarize({ name: "openssl", version: "3.0.13" })).toBe("version=3.0.13");
    expect(summarize({ process_name: "sshd", port: 22 })).toBe("process_name=sshd");
    expect(summarize("raw")).toBe("raw");
    expect(summarize(null)).toBe("");
  });
});
