// Remote integration settings (D-039): which fields each integration accepts, client-side checks that
// mirror internal/intsettings validation, the settings that belong to one panel, and the apply state
// shown after saving.
import type { IntegrationName, IntegrationSetting, IntegrationSettingInput, IntegrationSettingsHost } from "@/api/integrationSettings";

export type ConfigField = "endpoint" | "username" | "password" | "database";

/** Fields per integration (semantic-conventions §6.2; docker and iis have none besides enabled). */
export const CONFIG_FIELDS: Record<IntegrationName, readonly ConfigField[]> = {
  nginx: ["endpoint"],
  redis: ["endpoint", "username", "password"],
  mysql: ["endpoint", "username", "password"],
  postgresql: ["endpoint", "username", "password", "database"],
  docker: [],
  mssql: ["endpoint", "username", "password"],
  iis: [],
};

export function isConfigurable(id: string | undefined): id is IntegrationName {
  return !!id && Object.hasOwn(CONFIG_FIELDS, id);
}

export const ENDPOINT_PLACEHOLDER: Record<IntegrationName, string> = {
  nginx: "http://127.0.0.1:8080/nginx_status",
  redis: "127.0.0.1:6379",
  mysql: "127.0.0.1:3306",
  postgresql: "127.0.0.1:5432",
  docker: "",
  mssql: "127.0.0.1:1433",
  iis: "",
};

export type EndpointError = "url" | "hostPort";

/** nginx: http(s) URL with a host; others: host:port, [v6]:port or unix:/absolute/path. Empty is valid (derived by the agent). */
export function endpointError(integration: IntegrationName, value: string): EndpointError | null {
  const v = value.trim();
  if (!v) return null;
  if (integration === "nginx") {
    try {
      const u = new URL(v);
      return (u.protocol === "http:" || u.protocol === "https:") && u.hostname ? null : "url";
    } catch {
      return "url";
    }
  }
  if (v.startsWith("unix:")) return v.length > 6 && v[5] === "/" ? null : "hostPort";
  const m = /^(\[[0-9a-fA-F:.]+\]|[^\s:[\]/]+):(\d{1,5})$/.exec(v);
  return m && Number(m[2]) >= 1 && Number(m[2]) <= 65535 ? null : "hostPort";
}

const hasMatch = (s: IntegrationSetting) => !!(s.match.port || s.match.container || s.match.endpoint || s.match.instance);

/** The host-scoped setting of one discovered instance (matched by instance only), as saved by the panel form. */
export function instanceSetting(items: readonly IntegrationSetting[], hostId: string, integration: string, instance: string): IntegrationSetting | undefined {
  return items.find(
    (s) => s.host_id === hostId && s.integration === integration && s.match.instance === instance && !s.match.port && !s.match.container && !s.match.endpoint,
  );
}

/** The host-scoped setting without a match: the "collect on this host" switch. */
export function hostSetting(items: readonly IntegrationSetting[], hostId: string, integration: string): IntegrationSetting | undefined {
  return items.find((s) => s.host_id === hostId && s.integration === integration && !hasMatch(s));
}

/** Whether the integration is switched off for the host (host setting, else an all-hosts setting). */
export function disabledOnHost(items: readonly IntegrationSetting[], hostId: string, integration: string): boolean {
  const host = hostSetting(items, hostId, integration);
  if (host) return !host.enabled;
  const all = items.find((s) => s.host_id === null && s.integration === integration && !hasMatch(s));
  return !!all && !all.enabled;
}

export interface ConfigDraft {
  endpoint: string;
  username: string;
  /** New password; "" keeps the stored one unless clearPassword. */
  password: string;
  clearPassword: boolean;
  database: string;
  enabled: boolean;
}

export function draftFrom(s: IntegrationSetting | undefined): ConfigDraft {
  return { endpoint: s?.endpoint ?? "", username: s?.username ?? "", password: "", clearPassword: false, database: s?.database ?? "", enabled: s?.enabled ?? true };
}

/** Request body for the instance setting; fields the integration does not accept are left out. */
export function instanceInput(integration: IntegrationName, hostId: string, instance: string, d: ConfigDraft, existing?: IntegrationSetting): IntegrationSettingInput {
  const fields = CONFIG_FIELDS[integration];
  const body: IntegrationSettingInput = { host_id: hostId, integration, match: { instance }, enabled: d.enabled };
  if (fields.includes("endpoint")) body.endpoint = d.endpoint.trim();
  if (fields.includes("username")) body.username = d.username.trim();
  if (fields.includes("database")) {
    body.database = d.database.trim();
    body.databases = existing?.databases ?? [];
  }
  if (fields.includes("password")) {
    if (d.clearPassword) body.password = "";
    else if (d.password !== "") body.password = d.password;
    else body.password = null; // keep
  }
  return body;
}

export type ApplyPhase = "idle" | "disabled" | "sent" | "awaiting_status";

/**
 * State after a save: "sent" until the agent reports the current revision, then "awaiting_status" until an
 * inventory snapshot newer than the save (and the agent's sync) carries the new integration status.
 * "disabled": the agent ignores remote config (integrations.remote_config: false).
 */
export function applyPhase(host: IntegrationSettingsHost | null | undefined, savedAt: number | null, snapshotTime: string | null | undefined): ApplyPhase {
  if (!host) return "idle";
  if (host.remote_config_disabled) return "disabled";
  // An agent that never reported a revision (older version, or no sync yet) only shows "sent" right after a save.
  if (host.revision !== host.applied_revision) return savedAt !== null || host.applied_revision !== "" ? "sent" : "idle";
  if (savedAt === null) return "idle";
  const snap = snapshotTime ? Date.parse(snapshotTime) : NaN;
  const applied = host.applied_at ? Date.parse(host.applied_at) : NaN;
  const since = Math.max(savedAt, Number.isNaN(applied) ? 0 : applied);
  return !Number.isNaN(snap) && snap > since ? "idle" : "awaiting_status";
}
