import { afterEach, describe, expect, it } from "vitest";
import type { ApmServiceAgent, ApmServiceAgents } from "@/api/apm";
import {
  agentProduct,
  agentRows,
  agentRowsCsv,
  attentionAgents,
  csvField,
  dismissNotice,
  filterAgentRows,
  indexAgents,
  isNoticeDismissed,
  noticeKey,
  outdatedVersions,
  serviceKey,
} from "./agent-versions";

const seen = "2026-09-15T12:00:00.000000000Z";

function agent(kind: ApmServiceAgent["kind"], status: ApmServiceAgent["status"], versions: [string, ApmServiceAgent["status"], number][], sdk_language = ""): ApmServiceAgent {
  return {
    kind,
    distro_name: kind === "third_party" ? "" : "openlog",
    sdk_name: "opentelemetry",
    sdk_language,
    status,
    instances: versions.reduce((n, v) => n + v[2], 0),
    last_seen: seen,
    versions: versions.map(([version, st, instances]) => ({ version, status: st, instances, spans: instances * 10, last_seen: seen })),
    versions_truncated: false,
    instrumentation_modules: [],
    upgrade: null,
  };
}

const node = agent("node", "outdated", [["0.1.31", "ok", 1], ["0.1.28", "outdated", 3]], "nodejs");
const java = agent("java", "unsupported", [["0.1.9", "unsupported", 2]], "java");
const otel = agent("third_party", "third_party", [["1.27.0", "third_party", 1]], "python");
const web: ApmServiceAgents = { service_name: "web", service_namespace: "shop", environment: "prod", status: "unsupported", agents: [node, java] };
const legacy: ApmServiceAgents = { service_name: "legacy", service_namespace: "", environment: "staging", status: "third_party", agents: [otel] };
const services = [web, legacy];

afterEach(() => localStorage.clear());

describe("agent versions", () => {
  it("names products and finds agents that need an upgrade", () => {
    expect(agentProduct(node)).toBe("openlog-node");
    expect(agentProduct(otel)).toBe("opentelemetry · python");
    const idx = indexAgents(services);
    const found = idx.get(serviceKey({ service_name: "web", service_namespace: "shop", environment: "prod" }));
    expect(attentionAgents(found).map((a) => a.kind)).toEqual(["java", "node"]);
    expect(outdatedVersions(node)).toEqual(["0.1.28"]);
    expect(attentionAgents(idx.get(serviceKey(legacy)))).toEqual([]);
    expect(attentionAgents(undefined)).toEqual([]);
  });

  it("flattens, filters and exports rows", () => {
    const rows = agentRows(services);
    expect(rows).toHaveLength(4);
    expect(filterAgentRows(rows, { outdatedOnly: true }).map((r) => r.version)).toEqual(["0.1.28", "0.1.9"]);
    expect(filterAgentRows(rows, { environment: "staging" }).map((r) => r.service_name)).toEqual(["legacy"]);
    expect(filterAgentRows(rows, { q: "javaagent 0.1" }).map((r) => r.version)).toEqual(["0.1.9"]);
    const csv = agentRowsCsv(rows.slice(0, 1), "0.1.31").split("\n");
    expect(csv[0]).toBe("service_name,service_namespace,environment,agent_kind,agent,version,status,instances,spans,last_seen,latest");
    expect(csv[1]).toBe(`web,shop,prod,node,openlog-node,0.1.31,ok,1,10,${seen},0.1.31`);
    expect(csvField('a,"b"')).toBe('"a,""b"""');
    expect(csvField("=HYPERLINK()")).toBe("'=HYPERLINK()");
  });

  it("remembers dismissed notices per latest version", () => {
    const key = noticeKey(web, "node", "0.1.31");
    expect(isNoticeDismissed(key)).toBe(false);
    dismissNotice(key);
    dismissNotice(key);
    expect(isNoticeDismissed(key)).toBe(true);
    expect(isNoticeDismissed(noticeKey(web, "node", "0.1.32"))).toBe(false);
    localStorage.setItem("openlog.apm.agent-notice.dismissed", "{broken");
    expect(isNoticeDismissed(key)).toBe(false);
  });
});
