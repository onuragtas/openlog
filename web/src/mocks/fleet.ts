// MSW handlers for the fleet API (docs/contracts/api.md "Fleet") with an in-memory fleet. Saving an
// automatic policy starts a rollout; every GET /fleet/summary advances it one step (hosts of the
// current wave update, one host fails in the second wave, waves advance, the rollout completes), so the
// UI and Playwright can watch a rollout progress without a backend.
import { http, HttpResponse, type HttpResponseResolver } from "msw";
import type { FleetHost, FleetHostPHPAgent, FleetPHPAgentMode, FleetPolicy, FleetRollout, FleetSummary } from "@/api/fleet";
import type { Role } from "@/api/roles";
import { compareVersions, isOpenRollout, isVersion, parseWaves } from "@/lib/fleet";
import { authenticate } from "./account";
import { formatTs } from "./fixtures";

const API = "*/api/v1/fleet";
const LATEST = "0.4.0";
const BETA = "0.5.0-beta.1";
const OLDEST_SUPPORTED = "0.3.0";
const RANK: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };

type Code = "invalid_argument" | "permission_denied" | "not_found" | "failed_precondition";
const STATUS: Record<Code, number> = { invalid_argument: 400, permission_denied: 403, not_found: 404, failed_precondition: 409 };
const fail = (code: Code, message: string) => HttpResponse.json({ error: { code, message } }, { status: STATUS[code] });

/** fnv32a(host_id + ":" + rollout_id) % 100, as the backend computes wave buckets. */
export function bucket(hostId: string, rolloutId: string): number {
  let h = 0x811c9dc5;
  for (const ch of new TextEncoder().encode(`${hostId}:${rolloutId}`)) {
    h ^= ch;
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h % 100;
}

interface MockState {
  policy: FleetPolicy;
  hosts: FleetHost[];
  rollouts: FleetRollout[];
  seq: number;
}

/** PHP inventory of a seeded host: every 4th web host runs PHP-FPM 8.2, some already have the PHP agent. */
function seedPHP(i: number, container: boolean, version: string, now: number): FleetHostPHPAgent {
  const base: FleetHostPHPAgent = {
    reported: !container,
    mode: "manual",
    agent_mode: container ? "" : "manual",
    source: container ? "" : "remote",
    capable: !container,
    reason: container ? "running in a container: update the image instead" : "",
    managed_by: "none",
    version: null,
    runtimes: [],
    update: null,
    override: null,
    status: container ? "not_reported" : "manual",
    status_target: null,
  };
  if (container || i % 4 !== 1) return base;
  const installed = i % 8 === 1 && version === LATEST;
  const packaged = i === 5;
  return {
    ...base,
    managed_by: packaged ? "package" : installed ? "fleet" : "none",
    version: installed || packaged ? version : null,
    runtimes: [
      { bin: "/usr/sbin/php-fpm8.2", version: "8.2.29", api: "20220829", zts: false, debug: false, libc: "glibc", scan_dir: "/etc/php/8.2/fpm/conf.d",
        module: "20220829-nts-glibc", supported: true, enabled: installed || packaged, loaded: installed || packaged, excluded: false },
      { bin: "/usr/bin/php8.2", version: "8.2.29", api: "20220829", zts: false, debug: false, libc: "glibc", scan_dir: "/etc/php/8.2/cli/conf.d",
        module: "20220829-nts-glibc", supported: true, enabled: installed || packaged, loaded: installed || packaged, excluded: false },
    ],
    update: installed ? { operation: "install", version, state: "applied", error: "", changed_at: formatTs(now - 2 * 86_400_000) } : null,
  };
}

function seedHosts(now: number): FleetHost[] {
  const hosts: FleetHost[] = [];
  for (let i = 0; i < 64; i++) {
    const container = i >= 60;
    const version = i < 42 ? "0.3.0" : i < 58 ? LATEST : i < 60 ? "0.2.0" : "0.3.0";
    const name = container ? `k8s-node-${i - 59}` : `web-${String(i + 1).padStart(2, "0")}`;
    hosts.push({
      php_agent: seedPHP(i, container, version, now),
      php_access: container
        ? null
        : {
            socket_group: "openlog-php",
            group: "openlog-php",
            group_exists: true,
            agent_member: true,
            grants: "auto",
            pools: [
              { pool: "www", php_version: "8.2", user: "www-data", unit: "php8.2-fpm.service", access: "ok" },
              ...(i === 1 ? [{ pool: "shop.example.com", php_version: "7.2", user: "shop", unit: "php7.2-fpm.service", access: "missing" }] : []),
            ],
          },
      host_id: `h-${String(i).padStart(4, "0")}-5d1e0c9a7f3b`,
      host_name: name,
      agent: {
        name: "openlog-infra-agent",
        version,
        commit: "3f9c2a1",
        os: "linux",
        arch: i % 5 === 0 ? "arm64" : "amd64",
        install_method: container ? "container" : i % 3 === 0 ? "deb" : "tarball",
        update_capable: !container,
      },
      update: { state: "idle", from_version: "", to_version: "", error: "", changed_at: null },
      first_seen_at: formatTs(now - 20 * 86_400_000),
      last_sync_at: formatTs(now - ((i * 37) % 280) * 1000),
      rollout_id: null,
      override: null,
      outdated: false,
      supported: true,
      status: "up_to_date",
      status_target: null,
    });
  }
  return hosts;
}

function seed(): MockState {
  const now = Date.now();
  return {
    policy: {
      mode: "notify",
      channel: "stable",
      target: "latest",
      pinned_version: null,
      waves: [10, 50, 100],
      wave_soak_minutes: 60,
      halt_failure_rate: 0.05,
      maintenance_windows: [],
      php_agent: { mode: "manual", version: "agent", reload: "none", exclude_bins: [], changed_at: null },
      is_default: false,
      updated_at: formatTs(now - 3 * 86_400_000),
      updated_by_email: "admin@openlog.local",
    },
    hosts: seedHosts(now),
    rollouts: [
      {
        id: "r-0000-previous",
        action: "upgrade",
        from_version: "0.2.0",
        to_version: "0.3.0",
        targets: {},
        waves: [10, 50, 100],
        current_wave: 2,
        wave_percent: 100,
        wave_started_at: formatTs(now - 30 * 86_400_000),
        next_wave_at: null,
        wave_soak_minutes: 60,
        halt_failure_rate: 0.05,
        state: "superseded",
        state_reason: "superseded by rollout to 0.3.0",
        counters: { pending: 0, attempted: 57, succeeded: 57, failed: 0, rolled_back: 0 },
        created_by_email: null,
        created_at: formatTs(now - 31 * 86_400_000),
        updated_at: formatTs(now - 30 * 86_400_000),
        ended_at: formatTs(now - 30 * 86_400_000),
      },
    ],
    seq: 1,
  };
}

let db = seed();

/** Restores the seed fleet (tests). */
export function resetMockFleet(): void {
  db = seed();
}

const current = (): FleetRollout | null => [...db.rollouts].reverse().find((r) => r.state !== "superseded") ?? null;

function hostTarget(h: FleetHost, r: FleetRollout | null): string | null {
  if (h.override?.action === "pin") return h.override.version;
  return r?.to_version ?? null;
}

function decide(h: FleetHost, r: FleetRollout | null): { status: FleetHost["status"]; target: string | null } {
  if (db.policy.mode === "off") return { status: "mode_off", target: null };
  if (db.policy.mode === "notify") return { status: "notify_only", target: null };
  if (h.override?.action === "hold") return { status: "hold", target: null };
  if (!h.agent.update_capable) return { status: "not_capable", target: null };
  const target = hostTarget(h, r);
  if (!target) return { status: "no_rollout", target: null };
  const cmp = compareVersions(h.agent.version, target);
  const rollback = h.override?.action === "pin" ? cmp > 0 : r?.action === "rollback";
  if (cmp === 0 || (!rollback && cmp > 0) || (rollback && cmp < 0)) return { status: "up_to_date", target: null };
  if (h.override?.action !== "pin" && r) {
    if (r.state === "paused") return { status: "rollout_paused", target };
    if (r.state === "halted") return { status: "rollout_halted", target };
    if ((h.update.state === "failed" || h.update.state === "rolled_back") && h.update.to_version === target && h.rollout_id === r.id) {
      return { status: "already_failed", target };
    }
    if (compareVersions(h.agent.version, "0.3.0") < 0 && r.action === "upgrade") return { status: "incompatible", target };
    if (r.state === "active" && bucket(h.host_id, r.id) >= r.wave_percent) return { status: "not_in_wave", target };
  }
  return { status: "offer", target };
}

/** The PHP agent decision (internal/fleet DecidePHP without waves and windows). */
function decidePHP(h: FleetHost): FleetHostPHPAgent {
  const p = h.php_agent;
  const mode: FleetPHPAgentMode = p.override?.mode ?? db.policy.php_agent.mode;
  const out = (status: FleetHostPHPAgent["status"], target: string | null = null): FleetHostPHPAgent => ({ ...p, mode, status, status_target: target });
  if (mode === "off") return out("mode_off");
  if (mode === "manual") return out("manual");
  const target = db.policy.php_agent.version === "agent" ? h.agent.version : db.policy.php_agent.version;
  if (!p.reported) return out("not_reported", target);
  if (p.managed_by === "package" || p.managed_by === "manual") return out("managed_elsewhere", target);
  if (!p.capable) return out("not_capable", target);
  if (p.version === target) return out("up_to_date", target);
  if (!p.runtimes.some((r) => r.supported && !r.excluded)) return out("no_php", target);
  return out("offer", target);
}

/** Offered PHP hosts install their target (one step per summary refresh). */
function stepPHP(): void {
  const now = formatTs(Date.now());
  for (const h of db.hosts) {
    const d = decidePHP(h);
    if (d.status === "offer" && d.status_target) {
      const op = h.php_agent.version ? "upgrade" : "install";
      h.php_agent = {
        ...h.php_agent,
        managed_by: "fleet",
        version: d.status_target,
        runtimes: h.php_agent.runtimes.map((r) => ({ ...r, enabled: !r.excluded, loaded: !r.excluded })),
        update: { operation: op, version: d.status_target, state: "applied", error: "", changed_at: now },
      };
    } else if (d.status === "mode_off" && h.php_agent.managed_by === "fleet") {
      h.php_agent = {
        ...h.php_agent,
        managed_by: "none",
        version: null,
        runtimes: h.php_agent.runtimes.map((r) => ({ ...r, enabled: false, loaded: false })),
        update: { operation: "uninstall", version: h.php_agent.version ?? "", state: "uninstalled", error: "", changed_at: now },
      };
    }
  }
}

function view(h: FleetHost): FleetHost {
  const r = current();
  const d = decide(h, r);
  return {
    ...h,
    outdated: compareVersions(h.agent.version, LATEST) < 0,
    supported: compareVersions(h.agent.version, OLDEST_SUPPORTED) >= 0,
    status: d.status,
    status_target: d.target,
    php_agent: decidePHP(h),
  };
}

function recount(r: FleetRollout): void {
  const c = { pending: 0, attempted: 0, succeeded: 0, failed: 0, rolled_back: 0 };
  for (const h of db.hosts) {
    const mine = h.rollout_id === r.id;
    if (mine && h.agent.version === r.to_version) {
      c.succeeded++;
      c.attempted++;
      continue;
    }
    if (mine && h.update.state === "failed") {
      c.failed++;
      c.attempted++;
      continue;
    }
    const d = decide(h, { ...r, state: "active", wave_percent: 100 });
    if (d.status === "offer") c.pending++;
  }
  r.counters = c;
}

/** One simulation step: offered hosts of the current wave finish their update. */
function step(): void {
  const r = current();
  if (!r || r.state !== "active" || db.policy.mode !== "auto") return;
  const now = Date.now();
  const offered = db.hosts.filter((h) => decide(h, r).status === "offer" && h.override?.action !== "pin");
  let failedOne = r.counters.failed > 0;
  for (const h of offered.slice(0, 8)) {
    h.rollout_id = r.id;
    if (r.action === "upgrade" && r.current_wave === 1 && !failedOne) {
      failedOne = true;
      h.update = { state: "failed", from_version: h.agent.version, to_version: r.to_version ?? "", error: "self-test failed: exit status 1", changed_at: formatTs(now) };
      continue;
    }
    h.update = { state: "succeeded", from_version: h.agent.version, to_version: r.to_version ?? "", error: "", changed_at: formatTs(now) };
    h.agent = { ...h.agent, version: r.to_version ?? h.agent.version };
  }
  recount(r);
  const waveDone = db.hosts.every((h) => decide(h, r).status !== "offer");
  if (r.counters.pending === 0) {
    r.state = "completed";
    r.next_wave_at = null;
    r.ended_at = formatTs(now);
  } else if (waveDone && r.current_wave < r.waves.length - 1) {
    r.current_wave++;
    r.wave_percent = r.waves[r.current_wave]!;
    r.wave_started_at = formatTs(now);
    r.next_wave_at = r.current_wave < r.waves.length - 1 ? formatTs(now + r.wave_soak_minutes * 60_000) : null;
  }
  r.updated_at = formatTs(now);
}

function startRollout(action: "upgrade" | "rollback", from: string | null, to: string, email: string | null): FleetRollout {
  const now = Date.now();
  for (const r of db.rollouts) {
    if (isOpenRollout(r.state)) {
      r.state = "superseded";
      r.state_reason = `superseded by ${action} to ${to}`;
      r.ended_at = formatTs(now);
    }
  }
  const r: FleetRollout = {
    id: `r-${++db.seq}-${Math.random().toString(16).slice(2, 10)}`,
    action,
    from_version: from,
    to_version: to,
    targets: {},
    waves: [...db.policy.waves],
    current_wave: 0,
    wave_percent: db.policy.waves[0]!,
    wave_started_at: formatTs(now),
    next_wave_at: db.policy.waves.length > 1 ? formatTs(now + db.policy.wave_soak_minutes * 60_000) : null,
    wave_soak_minutes: db.policy.wave_soak_minutes,
    halt_failure_rate: db.policy.halt_failure_rate,
    state: "active",
    state_reason: "",
    counters: { pending: 0, attempted: 0, succeeded: 0, failed: 0, rolled_back: 0 },
    created_by_email: email,
    created_at: formatTs(now),
    updated_at: formatTs(now),
    ended_at: null,
  };
  db.rollouts.push(r);
  recount(r);
  return r;
}

function summary(): FleetSummary {
  const hosts = db.hosts.map(view);
  const versions = new Map<string, number>();
  const notCapable = new Map<string, number>();
  for (const h of hosts) {
    versions.set(h.agent.version, (versions.get(h.agent.version) ?? 0) + 1);
    if (!h.agent.update_capable) notCapable.set(h.agent.install_method, (notCapable.get(h.agent.install_method) ?? 0) + 1);
  }
  const outdated = hosts.filter((h) => h.outdated).length;
  const release = (version: string, channel: "stable" | "beta") => ({
    version,
    channel,
    released_at: formatTs(Date.now() - 6 * 86_400_000),
    notes_url: `https://github.com/onuragtas/openlog/releases/tag/v${version}`,
  });
  return {
    total_hosts: hosts.length,
    active_hosts: hosts.length,
    update_capable: hosts.filter((h) => h.agent.update_capable).length,
    not_update_capable: [...notCapable].map(([reason, n]) => ({ reason, hosts: n })),
    outdated,
    unsupported: hosts.filter((h) => !h.supported).length,
    in_progress: 0,
    failed: hosts.filter((h) => h.update.state === "failed" || h.update.state === "rolled_back").length,
    held: hosts.filter((h) => h.override?.action === "hold").length,
    pinned: hosts.filter((h) => h.override?.action === "pin").length,
    versions: [...versions]
      .sort(([a], [b]) => compareVersions(b, a))
      .map(([version, n]) => ({
        version,
        hosts: n,
        latest: version === LATEST,
        outdated: compareVersions(version, LATEST) < 0,
        supported: compareVersions(version, OLDEST_SUPPORTED) >= 0,
      })),
    latest: { stable: release(LATEST, "stable"), beta: db.policy.channel === "beta" ? release(BETA, "beta") : release(BETA, "beta") },
    target: db.policy.target === "patch" ? null : release(db.policy.target === "pinned" ? (db.policy.pinned_version ?? LATEST) : LATEST, "stable"),
    oldest_supported_version: OLDEST_SUPPORTED,
    policy_mode: db.policy.mode,
    update_available: outdated > 0,
    stale_after_seconds: 86_400,
    catalog: {
      status: "ok",
      source: "https://github.com/onuragtas/openlog/releases/latest/download/index.json",
      checked_at: formatTs(Date.now() - 120_000),
      last_success_at: formatTs(Date.now() - 120_000),
      error: "",
      releases: 6,
      warnings: [],
    },
    current_rollout: current(),
  };
}

type Info = Parameters<HttpResponseResolver>[0];
type Ctx = Exclude<ReturnType<typeof authenticate>, Response>;

function read(fn: (info: Info, ctx: Ctx) => Response | Promise<Response>): HttpResponseResolver {
  return (info) => {
    const ctx = authenticate(info.request);
    return ctx instanceof Response ? ctx : fn(info, ctx);
  };
}

function write(fn: (info: Info, ctx: Ctx) => Response | Promise<Response>): HttpResponseResolver {
  return read((info, ctx) => {
    if (ctx.kind !== "session") return fail("permission_denied", "this operation requires a signed-in user; API keys are read-only");
    if (RANK[ctx.role] < RANK.admin) return fail("permission_denied", `your role (${ctx.role}) does not allow this operation`);
    return fn(info, ctx);
  });
}

async function body<T>(request: Request): Promise<Partial<T>> {
  try {
    return ((await request.json()) as Partial<T>) ?? {};
  } catch {
    return {};
  }
}

export const fleetHandlers = [
  http.get(`${API}/summary`, read(() => {
    step();
    stepPHP();
    return HttpResponse.json(summary());
  })),

  http.get(`${API}/policy`, read(() => HttpResponse.json(db.policy))),

  http.put(`${API}/policy`, write(async ({ request }) => {
    const p = await body<FleetPolicy>(request);
    if (!p.mode || !["off", "notify", "auto"].includes(p.mode)) return fail("invalid_argument", "mode must be off, notify or auto");
    const waves = parseWaves((p.waves ?? []).join(","));
    if (waves.error) return fail("invalid_argument", "waves must be strictly increasing percentages between 1 and 100 ending at 100");
    if (p.target === "pinned" && !p.pinned_version) return fail("invalid_argument", "pinned_version is required when target is pinned");
    let php = db.policy.php_agent;
    if (p.php_agent) {
      const next = p.php_agent;
      if (!["off", "manual", "auto"].includes(next.mode)) return fail("invalid_argument", "php_agent.mode must be off, manual or auto");
      const version = (next.version ?? "agent").trim() || "agent";
      if (version !== "agent" && !isVersion(version)) return fail("invalid_argument", "php_agent.version must be agent or a SemVer version");
      if ((next.exclude_bins ?? []).length > 50) return fail("invalid_argument", "php_agent.exclude_bins: at most 50 globs");
      const merged = { mode: next.mode, version: version.replace(/^v/, ""), reload: next.reload ?? "none", exclude_bins: next.exclude_bins ?? [] };
      const same = JSON.stringify(merged) === JSON.stringify({ mode: php.mode, version: php.version, reload: php.reload, exclude_bins: php.exclude_bins });
      php = { ...merged, changed_at: same ? php.changed_at : formatTs(Date.now()) };
    }
    db.policy = {
      ...db.policy,
      ...p,
      php_agent: php,
      pinned_version: p.target === "pinned" ? (p.pinned_version ?? null) : null,
      is_default: false,
      updated_at: formatTs(Date.now()),
      updated_by_email: "admin@openlog.local",
    } as FleetPolicy;
    const cur = current();
    if (db.policy.mode === "auto" && (!cur || cur.state === "superseded" || (cur.action === "upgrade" && cur.to_version !== LATEST))) {
      if (db.hosts.some((h) => h.agent.update_capable && compareVersions(h.agent.version, LATEST) < 0)) startRollout("upgrade", "0.3.0", LATEST, null);
    }
    return HttpResponse.json(db.policy);
  })),

  http.get(`${API}/hosts`, read(({ request }) => {
    const url = new URL(request.url);
    const q = (url.searchParams.get("q") ?? "").toLowerCase();
    const version = url.searchParams.get("version") ?? "";
    const state = url.searchParams.get("state") ?? "";
    const limit = Math.min(Number(url.searchParams.get("limit") ?? 100) || 100, 1000);
    const start = Number(url.searchParams.get("cursor") ?? 0) || 0;
    const list = db.hosts
      .map(view)
      .filter((h) => (!q || h.host_name.toLowerCase().includes(q) || h.host_id.includes(q)) && (!version || h.agent.version === version) && (!state || h.update.state === state))
      .sort((a, b) => a.host_name.localeCompare(b.host_name));
    const page = list.slice(start, start + limit);
    return HttpResponse.json({ hosts: page, next_cursor: start + limit < list.length ? String(start + limit) : null });
  })),

  http.put(`${API}/hosts/:hostId/override`, write(async ({ request, params }) => {
    const h = db.hosts.find((x) => x.host_id === params.hostId);
    if (!h) return fail("not_found", "not found");
    const b = await body<{ action: string; version: string }>(request);
    if (b.action !== "hold" && b.action !== "pin") return fail("invalid_argument", "action must be hold or pin");
    if (b.action === "pin" && !b.version) return fail("invalid_argument", "version: required");
    h.override = { action: b.action, version: b.action === "pin" ? (b.version ?? null) : null, updated_at: formatTs(Date.now()) };
    return HttpResponse.json(h.override);
  })),

  http.delete(`${API}/hosts/:hostId/override`, write(({ params }) => {
    const h = db.hosts.find((x) => x.host_id === params.hostId);
    if (h) h.override = null;
    return new HttpResponse(null, { status: 204 });
  })),

  http.put(`${API}/hosts/:hostId/php-agent`, write(async ({ request, params }) => {
    const h = db.hosts.find((x) => x.host_id === params.hostId);
    if (!h) return fail("not_found", "not found");
    const b = await body<{ mode: FleetPHPAgentMode }>(request);
    if (!b.mode || !["off", "manual", "auto"].includes(b.mode)) return fail("invalid_argument", "mode must be off, manual or auto");
    h.php_agent = { ...h.php_agent, override: { mode: b.mode, updated_at: formatTs(Date.now()) } };
    return HttpResponse.json(h.php_agent.override);
  })),

  http.delete(`${API}/hosts/:hostId/php-agent`, write(({ params }) => {
    const h = db.hosts.find((x) => x.host_id === params.hostId);
    if (h) h.php_agent = { ...h.php_agent, override: null };
    return new HttpResponse(null, { status: 204 });
  })),

  http.get(`${API}/rollouts`, read(() => HttpResponse.json({ rollouts: [...db.rollouts].reverse().slice(0, 10) }))),

  http.post(`${API}/rollouts/:id/pause`, write(({ params }) => {
    const r = db.rollouts.find((x) => x.id === params.id);
    if (!r) return fail("not_found", "not found");
    if (r.state !== "active") return fail("failed_precondition", `only an active rollout can be paused (rollout is ${r.state})`);
    r.state = "paused";
    r.state_reason = "paused by admin@openlog.local";
    r.next_wave_at = null;
    return HttpResponse.json(r);
  })),

  http.post(`${API}/rollouts/:id/resume`, write(({ params }) => {
    const r = db.rollouts.find((x) => x.id === params.id);
    if (!r) return fail("not_found", "not found");
    if (r.state !== "paused" && r.state !== "halted") return fail("failed_precondition", `only a paused or halted rollout can be resumed (rollout is ${r.state})`);
    r.state = "active";
    r.state_reason = "";
    r.wave_started_at = formatTs(Date.now());
    return HttpResponse.json(r);
  })),

  http.post(`${API}/rollouts/:id/deploy-now`, write(({ params }) => {
    const r = db.rollouts.find((x) => x.id === params.id);
    if (!r) return fail("not_found", "not found");
    if (r.state !== "active") return fail("failed_precondition", `only an active rollout can be deployed to all agents (rollout is ${r.state})`);
    if (r.current_wave >= r.waves.length - 1) return fail("failed_precondition", "the rollout is already in its last wave");
    r.current_wave = r.waves.length - 1;
    r.wave_percent = r.waves[r.current_wave] ?? 100;
    r.wave_started_at = formatTs(Date.now());
    r.next_wave_at = null;
    return HttpResponse.json(r);
  })),

  http.post(`${API}/rollback`, write(async ({ request }) => {
    const b = await body<{ to_version: string }>(request);
    const to = b.to_version ?? "";
    const cur = current();
    const from = cur?.action === "upgrade" ? cur.to_version : LATEST;
    if (!to || compareVersions(to, from ?? LATEST) >= 0) return fail("invalid_argument", `to_version must be lower than ${from}`);
    if (compareVersions(to, OLDEST_SUPPORTED) < 0) return fail("failed_precondition", `${from} cannot be rolled back below ${OLDEST_SUPPORTED} (rollback_floor)`);
    return HttpResponse.json(startRollout("rollback", from, to, "admin@openlog.local"), { status: 201 });
  })),
];
