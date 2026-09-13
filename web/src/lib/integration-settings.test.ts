import { describe, expect, it } from "vitest";
import type { IntegrationSetting, IntegrationSettingsHost } from "@/api/integrationSettings";
import { applyPhase, disabledOnHost, draftFrom, endpointError, hostSetting, instanceInput, instanceSetting } from "./integration-settings";

function setting(p: Partial<IntegrationSetting>): IntegrationSetting {
  return {
    id: "s", host_id: "h1", integration: "redis", match: { port: null, container: "", endpoint: "", instance: "" }, enabled: true, endpoint: "",
    username: "", password_set: false, database: "", databases: [], created_at: "", updated_at: "", updated_by_email: "", ...p,
  };
}

describe("endpointError", () => {
  it("checks nginx URLs and host:port / unix endpoints", () => {
    expect(endpointError("nginx", "")).toBeNull();
    expect(endpointError("nginx", "http://127.0.0.1:8080/nginx_status")).toBeNull();
    expect(endpointError("nginx", "127.0.0.1:80")).toBe("url");
    expect(endpointError("nginx", "ftp://x/status")).toBe("url");
    expect(endpointError("redis", "127.0.0.1:6379")).toBeNull();
    expect(endpointError("redis", "[::1]:6379")).toBeNull();
    expect(endpointError("mysql", "unix:/run/mysqld/mysqld.sock")).toBeNull();
    expect(endpointError("mysql", "unix:relative.sock")).toBe("hostPort");
    expect(endpointError("postgresql", "db:99999")).toBe("hostPort");
    expect(endpointError("postgresql", "http://db:5432")).toBe("hostPort");
  });
});

describe("settings of a panel", () => {
  const items = [
    setting({ id: "all", host_id: null, enabled: false }),
    setting({ id: "inst", match: { port: null, container: "", endpoint: "", instance: "/usr/bin/redis-server" }, username: "u", password_set: true }),
    setting({ id: "other-host", host_id: "h2", match: { port: null, container: "", endpoint: "", instance: "/usr/bin/redis-server" } }),
    setting({ id: "port", match: { port: 6380, container: "", endpoint: "", instance: "/usr/bin/redis-server" } }),
  ];

  it("finds the instance and host settings", () => {
    expect(instanceSetting(items, "h1", "redis", "/usr/bin/redis-server")?.id).toBe("inst");
    expect(instanceSetting(items, "h1", "mysql", "/usr/bin/redis-server")).toBeUndefined();
    expect(hostSetting(items, "h1", "redis")).toBeUndefined();
    // No host setting: the all-hosts setting decides.
    expect(disabledOnHost(items, "h1", "redis")).toBe(true);
    expect(disabledOnHost([...items, setting({ id: "host", enabled: true })], "h1", "redis")).toBe(false);
  });

  it("builds request bodies with keep/clear/set password semantics", () => {
    const d = draftFrom(items[1]);
    expect(d).toMatchObject({ username: "u", password: "", enabled: true });
    expect(instanceInput("redis", "h1", "/usr/bin/redis-server", { ...d, endpoint: " 127.0.0.1:6379 " })).toEqual({
      host_id: "h1", integration: "redis", match: { instance: "/usr/bin/redis-server" }, enabled: true, endpoint: "127.0.0.1:6379", username: "u", password: null,
    });
    expect(instanceInput("redis", "h1", "i", { ...d, password: "pw" }).password).toBe("pw");
    expect(instanceInput("redis", "h1", "i", { ...d, password: "pw", clearPassword: true }).password).toBe("");
    const nginx = instanceInput("nginx", "h1", "/usr/sbin/nginx", { ...d, endpoint: "http://127.0.0.1/status" });
    expect(nginx).not.toHaveProperty("username");
    expect(nginx).not.toHaveProperty("password");
    expect(instanceInput("postgresql", "h1", "i", { ...d, database: "app" })).toMatchObject({ database: "app", databases: [] });
  });
});

describe("applyPhase", () => {
  const host = (p: Partial<IntegrationSettingsHost>): IntegrationSettingsHost => ({
    host_id: "h1", revision: "sha256:b", applied_revision: "sha256:b", applied_at: "2026-09-13T10:00:00Z", remote_config_disabled: false, ...p,
  });
  const saved = Date.parse("2026-09-13T10:00:30Z");

  it("follows a save until the agent applied it and reported the status", () => {
    expect(applyPhase(null, saved, null)).toBe("idle");
    expect(applyPhase(host({ remote_config_disabled: true }), saved, null)).toBe("disabled");
    expect(applyPhase(host({ applied_revision: "sha256:a" }), saved, "2026-09-13T10:00:00Z")).toBe("sent");
    expect(applyPhase(host({ applied_at: "2026-09-13T10:01:00Z" }), saved, "2026-09-13T10:00:50Z")).toBe("awaiting_status");
    expect(applyPhase(host({ applied_at: "2026-09-13T10:01:00Z" }), saved, "2026-09-13T10:01:10Z")).toBe("idle");
    // Nothing saved on this page: an applied revision is simply the steady state.
    expect(applyPhase(host({}), null, "2026-09-13T09:00:00Z")).toBe("idle");
    // A change saved elsewhere that the agent has not applied yet.
    expect(applyPhase(host({ applied_revision: "sha256:a" }), null, null)).toBe("sent");
  });
});
