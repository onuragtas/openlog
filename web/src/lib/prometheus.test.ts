import { describe, expect, it } from "vitest";
import { prometheusTargets, targetExplorerFilters } from "./prometheus";

const attrs = (job: string, instance: string, source = "static", host = "h1") => ({
  "host.id": host,
  "host.name": `${host}.example`,
  "service.name": job,
  "resource.service.instance.id": instance,
  "resource.openlog.scrape.source": source,
});

describe("prometheusTargets", () => {
  it("takes the latest point of each up series and puts down targets first", () => {
    const out = prometheusTargets([
      { attributes: attrs("node", "127.0.0.1:9100"), points: [[1000, 0], [2000, 1]] },
      { attributes: attrs("app", "10.0.0.5:8080", "pod"), points: [[1000, 1], [3000, 0]] },
      { attributes: attrs("api", "172.17.0.2:9090", "container"), points: [[1000, 1]] },
      { attributes: attrs("gone", "x:1"), points: [] },
    ]);
    expect(out.map((t) => [t.job, t.up, t.lastSeen, t.source])).toEqual([
      ["app", false, 3000, "pod"],
      ["api", true, 1000, "container"],
      ["node", true, 2000, "static"],
    ]);
    expect(out[0]!.hostName).toBe("h1.example");
  });

  it("ignores unknown sources and falls back to the host id as name", () => {
    const [t] = prometheusTargets([{ attributes: { "host.id": "h9", "service.name": "x", "resource.openlog.scrape.source": "weird" }, points: [[5, 1]] }]);
    expect(t).toMatchObject({ hostName: "h9", source: "", instance: "", up: true });
  });

  it("builds explorer filters for one target", () => {
    const [t] = prometheusTargets([{ attributes: attrs("node", "127.0.0.1:9100"), points: [[1, 1]] }]);
    expect(targetExplorerFilters(t!)).toEqual([
      { key: "resource.openlog.integration.id", op: "=", value: "prometheus" },
      { key: "service.name", op: "=", value: "node" },
      { key: "resource.service.instance.id", op: "=", value: "127.0.0.1:9100" },
      { key: "host.id", op: "=", value: "h1" },
    ]);
  });
});
