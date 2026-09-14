import * as path from 'node:path';
import { parseLogLevel, type LogLevel } from './diag';

/** Protocol values for OPENLOG_PROTOCOL / `protocol`. */
export const PROTOCOL_HTTP = 'http/protobuf';
export const PROTOCOL_GRPC = 'grpc';
export type Protocol = typeof PROTOCOL_HTTP | typeof PROTOCOL_GRPC;

/** Ingest authentication header (docs/contracts/config.md). */
export const LICENSE_KEY_HEADER = 'openlog-license-key';

export type DbQueryTextMode = 'sanitized' | 'raw' | 'off';

const DEFAULT_HTTP_ENDPOINT = 'http://localhost:4318';
const DEFAULT_GRPC_ENDPOINT = 'http://localhost:4317';

/** Options of `start()`. Every field overrides the corresponding environment variable. */
export interface OpenlogOptions {
  /** OPENLOG_ENABLED (OTEL_SDK_DISABLED). false: start() installs nothing. */
  enabled?: boolean;
  /** OPENLOG_LICENSE_KEY: sent as header openlog-license-key. */
  licenseKey?: string;
  /** OPENLOG_ENDPOINT (OTEL_EXPORTER_OTLP_ENDPOINT): OTLP base URL; http/protobuf appends /v1/{traces,metrics,logs}. */
  endpoint?: string;
  /** OPENLOG_PROTOCOL (OTEL_EXPORTER_OTLP_PROTOCOL): http/protobuf (default) or grpc. */
  protocol?: string;
  /** OPENLOG_COMPRESSION (OTEL_EXPORTER_OTLP_COMPRESSION): gzip (default) or none. */
  compression?: string;
  /** Extra export headers (merged over OTEL_EXPORTER_OTLP_HEADERS; the license key header wins). */
  headers?: Record<string, string>;
  serviceName?: string;
  serviceVersion?: string;
  serviceNamespace?: string;
  /** deployment.environment.name */
  environment?: string;
  /** OPENLOG_SAMPLING_RATIO: parent-based head sampling ratio of new traces (0..1). */
  samplingRatio?: number;
  /** OPENLOG_SAMPLING_RV: new traces write explicit randomness (tracestate ot=rv). */
  samplingRV?: boolean;
  /** OPENLOG_LOG_LEVEL (OTEL_LOG_LEVEL): agent diagnostics on stderr. */
  logLevel?: LogLevel;
  resourceAttributes?: Record<string, string>;
  /** OPENLOG_HOST_ID: explicit host.id instead of detection. */
  hostId?: string;
  /** OPENLOG_RUNTIME_METRICS: Node.js runtime metrics (default true). */
  runtimeMetrics?: boolean;
  /** OPENLOG_METRIC_EXPORT_INTERVAL (Go duration, e.g. 60s; OTEL_METRIC_EXPORT_INTERVAL in ms). */
  metricIntervalMs?: number;
  /** OPENLOG_SHUTDOWN_TIMEOUT: bound of the final flush (default 5s). */
  shutdownTimeoutMs?: number;
  /** Timeout of one export request (default 10s). */
  exportTimeoutMs?: number;
  /** OPENLOG_HOST_ROOT: prefix for host files (/etc/machine-id, …). */
  hostRoot?: string;
  /** OPENLOG_INFRA_STATE_DIR */
  infraStateDir?: string;
  /** OPENLOG_INFRA_RUNTIME_DIR */
  infraRuntimeDir?: string;
  /** OPENLOG_STATE_DIR: where this agent persists a generated host id. */
  stateDir?: string;
  /** OPENLOG_DB_QUERY_TEXT: sanitized (default), raw or off. */
  dbQueryText?: DbQueryTextMode;
  /** OPENLOG_LOGS_CONSOLE: also export console.* calls as OTLP logs (default false). */
  logsConsole?: boolean;
  /** OPENLOG_LOGS_EXPORT: export pino/winston records as OTLP logs (default true; correlation ids are always added). */
  logsExport?: boolean;
  /**
   * OPENLOG_INSTRUMENTATIONS_DISABLED (OTEL_NODE_DISABLED_INSTRUMENTATIONS): names to disable, e.g. ["aws-sdk", "graphql"].
   * Full package names (@opentelemetry/instrumentation-pg) are accepted too.
   */
  disabledInstrumentations?: string[];
  /** Per-instrumentation configuration merged over the agent defaults, keyed by short name (http, pg, …). */
  instrumentationConfig?: Record<string, Record<string, unknown>>;
  /** OPENLOG_HTTP_IGNORE_PATHS: incoming request paths without spans (exact match, e.g. /healthz). */
  httpIgnorePaths?: string[];
  /** OPENLOG_SHUTDOWN_ON_SIGNAL: the register entry point flushes on SIGTERM/SIGINT (default true). */
  shutdownOnSignal?: boolean;
}

export interface Config {
  enabled: boolean;
  licenseKey: string;
  endpoint: string;
  protocol: Protocol;
  compression: 'gzip' | 'none';
  headers: Record<string, string>;
  serviceName: string;
  serviceVersion: string;
  serviceNamespace: string;
  environment: string;
  samplingRatio: number;
  samplingRV: boolean;
  logLevel: LogLevel;
  resourceAttributes: Record<string, string>;
  hostId: string;
  runtimeMetrics: boolean;
  metricIntervalMs: number;
  shutdownTimeoutMs: number;
  exportTimeoutMs: number;
  hostRoot: string;
  infraStateDir: string;
  infraRuntimeDir: string;
  stateDir: string;
  dbQueryText: DbQueryTextMode;
  logsConsole: boolean;
  logsExport: boolean;
  disabledInstrumentations: string[];
  instrumentationConfig: Record<string, Record<string, unknown>>;
  httpIgnorePaths: string[];
  shutdownOnSignal: boolean;
}

export class ConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ConfigError';
  }
}

export type Env = Record<string, string | undefined>;

/** Parses the W3C-baggage-like "k1=v1,k2=v2" format (values may be percent-encoded). */
export function parseKV(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of s.split(',')) {
    const idx = part.indexOf('=');
    if (idx < 0) continue;
    const k = part.slice(0, idx).trim();
    if (!k) continue;
    let v = part.slice(idx + 1).trim();
    try {
      v = decodeURIComponent(v);
    } catch {
      // keep the raw value, like Go's url.PathUnescape error path
    }
    out[k] = v;
  }
  return out;
}

const DURATION_UNITS: Record<string, number> = { ns: 1e-6, us: 1e-3, 'µs': 1e-3, 'μs': 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 };

/** Parses a Go duration ("1m30s", "500ms", "1.5s") to milliseconds; undefined when invalid. */
export function parseGoDuration(s: string): number | undefined {
  const re = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/gy;
  let total = 0;
  let pos = 0;
  const str = s.trim();
  if (!str) return undefined;
  for (;;) {
    re.lastIndex = pos;
    const m = re.exec(str);
    if (!m) break;
    total += parseFloat(m[1]) * DURATION_UNITS[m[2]];
    pos = re.lastIndex;
    if (pos === str.length) return total;
  }
  if (str === '0') return 0;
  return undefined;
}

function parseBool(v: string): boolean | undefined {
  // strconv.ParseBool
  switch (v) {
    case '1':
    case 't':
    case 'T':
    case 'true':
    case 'TRUE':
    case 'True':
      return true;
    case '0':
    case 'f':
    case 'F':
    case 'false':
    case 'FALSE':
    case 'False':
      return false;
  }
  return undefined;
}

function list(v: string): string[] {
  return v
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean);
}

/** Resolves defaults < OTEL_* < OPENLOG_* < options (same precedence as the Go agent). */
export function loadConfig(env: Env = process.env, opts: OpenlogOptions = {}): { config: Config; warnings: string[] } {
  const warnings: string[] = [];
  const warn = (m: string): void => {
    warnings.push(m);
  };
  const get = (...names: string[]): [string, string] | undefined => {
    for (const n of names) {
      const v = env[n];
      if (v !== undefined && v.trim() !== '') return [v.trim(), n];
    }
    return undefined;
  };
  const c: Config = {
    enabled: true,
    licenseKey: '',
    endpoint: '',
    protocol: PROTOCOL_HTTP,
    compression: 'gzip',
    headers: {},
    serviceName: '',
    serviceVersion: '',
    serviceNamespace: '',
    environment: '',
    samplingRatio: 1,
    samplingRV: false,
    logLevel: 'warn',
    resourceAttributes: {},
    hostId: '',
    runtimeMetrics: true,
    metricIntervalMs: 60_000,
    shutdownTimeoutMs: 5_000,
    exportTimeoutMs: 10_000,
    hostRoot: '/',
    infraStateDir: '/var/lib/openlog-infra-agent',
    infraRuntimeDir: '/run/openlog-infra-agent',
    stateDir: '',
    dbQueryText: 'sanitized',
    logsConsole: false,
    logsExport: true,
    disabledInstrumentations: [],
    instrumentationConfig: {},
    httpIgnorePaths: [],
    shutdownOnSignal: true,
  };
  let protocol = '';
  let compression = '';
  const boolVar = (set: (b: boolean) => void, ...names: string[]): void => {
    const g = get(...names);
    if (!g) return;
    const b = parseBool(g[0]);
    if (b === undefined) warn(`${g[1]}=${JSON.stringify(g[0])} is not a boolean; ignored`);
    else set(b);
  };
  const durVar = (set: (ms: number) => void, name: string): void => {
    const g = get(name);
    if (!g) return;
    const ms = parseGoDuration(g[0]);
    if (ms === undefined || ms <= 0) warn(`${name}=${JSON.stringify(g[0])} is not a positive duration; ignored`);
    else set(ms);
  };

  // ---- OTEL_* (lower precedence) ----
  const sdkDisabled = get('OTEL_SDK_DISABLED');
  if (sdkDisabled) {
    const b = parseBool(sdkDisabled[0]);
    if (b !== undefined) c.enabled = !b;
  }
  let g = get('OTEL_EXPORTER_OTLP_ENDPOINT');
  if (g) c.endpoint = g[0];
  if ((g = get('OTEL_EXPORTER_OTLP_PROTOCOL'))) protocol = g[0];
  if ((g = get('OTEL_EXPORTER_OTLP_COMPRESSION'))) compression = g[0];
  if ((g = get('OTEL_EXPORTER_OTLP_HEADERS'))) Object.assign(c.headers, parseKV(g[0]));
  if ((g = get('OTEL_SERVICE_NAME'))) c.serviceName = g[0];
  if ((g = get('OTEL_RESOURCE_ATTRIBUTES'))) Object.assign(c.resourceAttributes, parseKV(g[0]));
  if ((g = get('OTEL_TRACES_SAMPLER_ARG'))) {
    const sampler = get('OTEL_TRACES_SAMPLER')?.[0] ?? '';
    if (sampler === '' || sampler.endsWith('traceidratio')) {
      const r = Number(g[0]);
      if (!Number.isNaN(r)) c.samplingRatio = r;
    }
  }
  if ((g = get('OTEL_LOG_LEVEL'))) {
    const l = parseLogLevel(g[0]);
    if (l) c.logLevel = l;
  }
  if ((g = get('OTEL_METRIC_EXPORT_INTERVAL'))) {
    const ms = Number(g[0]);
    if (Number.isInteger(ms) && ms > 0) c.metricIntervalMs = ms;
  }
  if ((g = get('OTEL_NODE_DISABLED_INSTRUMENTATIONS'))) c.disabledInstrumentations = list(g[0]);

  // ---- OPENLOG_* ----
  boolVar((b) => (c.enabled = b), 'OPENLOG_ENABLED');
  if ((g = get('OPENLOG_LICENSE_KEY'))) c.licenseKey = g[0];
  if ((g = get('OPENLOG_ENDPOINT'))) c.endpoint = g[0];
  if ((g = get('OPENLOG_PROTOCOL'))) protocol = g[0];
  if ((g = get('OPENLOG_COMPRESSION'))) compression = g[0];
  if ((g = get('OPENLOG_SERVICE_NAME'))) c.serviceName = g[0];
  if ((g = get('OPENLOG_SERVICE_VERSION'))) c.serviceVersion = g[0];
  if ((g = get('OPENLOG_SERVICE_NAMESPACE'))) c.serviceNamespace = g[0];
  if ((g = get('OPENLOG_ENVIRONMENT'))) c.environment = g[0];
  if ((g = get('OPENLOG_SAMPLING_RATIO'))) {
    const r = Number(g[0]);
    if (Number.isNaN(r) || g[0] === '') warn(`${g[1]}=${JSON.stringify(g[0])} is not a number; ignored`);
    else c.samplingRatio = r;
  }
  if ((g = get('OPENLOG_LOG_LEVEL'))) {
    const l = parseLogLevel(g[0]);
    if (!l) warn(`${g[1]}: unknown log level ${JSON.stringify(g[0])} (debug, info, warn, error, off)`);
    else c.logLevel = l;
  }
  if ((g = get('OPENLOG_RESOURCE_ATTRIBUTES'))) Object.assign(c.resourceAttributes, parseKV(g[0]));
  if ((g = get('OPENLOG_HOST_ID'))) c.hostId = g[0];
  boolVar((b) => (c.runtimeMetrics = b), 'OPENLOG_RUNTIME_METRICS');
  durVar((ms) => (c.metricIntervalMs = ms), 'OPENLOG_METRIC_EXPORT_INTERVAL');
  durVar((ms) => (c.shutdownTimeoutMs = ms), 'OPENLOG_SHUTDOWN_TIMEOUT');
  if ((g = get('OPENLOG_HOST_ROOT'))) c.hostRoot = g[0];
  if ((g = get('OPENLOG_INFRA_STATE_DIR'))) c.infraStateDir = g[0];
  if ((g = get('OPENLOG_INFRA_RUNTIME_DIR'))) c.infraRuntimeDir = g[0];
  boolVar((b) => (c.samplingRV = b), 'OPENLOG_SAMPLING_RV');
  if ((g = get('OPENLOG_STATE_DIR'))) c.stateDir = g[0];
  if ((g = get('OPENLOG_DB_QUERY_TEXT'))) {
    const v = g[0].toLowerCase();
    if (v === 'sanitized' || v === 'raw' || v === 'off') c.dbQueryText = v;
    else warn(`${g[1]}=${JSON.stringify(g[0])}: use sanitized, raw or off; ignored`);
  }
  boolVar((b) => (c.logsConsole = b), 'OPENLOG_LOGS_CONSOLE');
  boolVar((b) => (c.logsExport = b), 'OPENLOG_LOGS_EXPORT');
  if ((g = get('OPENLOG_INSTRUMENTATIONS_DISABLED'))) c.disabledInstrumentations = list(g[0]);
  if ((g = get('OPENLOG_HTTP_IGNORE_PATHS'))) c.httpIgnorePaths = list(g[0]);
  boolVar((b) => (c.shutdownOnSignal = b), 'OPENLOG_SHUTDOWN_ON_SIGNAL');

  // ---- options ----
  const o = opts;
  if (o.enabled !== undefined) c.enabled = o.enabled;
  if (o.licenseKey !== undefined) c.licenseKey = o.licenseKey;
  if (o.endpoint !== undefined) c.endpoint = o.endpoint;
  if (o.protocol !== undefined) protocol = o.protocol;
  if (o.compression !== undefined) compression = o.compression;
  if (o.headers) Object.assign(c.headers, o.headers);
  if (o.serviceName !== undefined) c.serviceName = o.serviceName;
  if (o.serviceVersion !== undefined) c.serviceVersion = o.serviceVersion;
  if (o.serviceNamespace !== undefined) c.serviceNamespace = o.serviceNamespace;
  if (o.environment !== undefined) c.environment = o.environment;
  if (o.samplingRatio !== undefined) c.samplingRatio = o.samplingRatio;
  if (o.samplingRV !== undefined) c.samplingRV = o.samplingRV;
  if (o.logLevel !== undefined) c.logLevel = o.logLevel;
  if (o.resourceAttributes) Object.assign(c.resourceAttributes, o.resourceAttributes);
  if (o.hostId !== undefined) c.hostId = o.hostId;
  if (o.runtimeMetrics !== undefined) c.runtimeMetrics = o.runtimeMetrics;
  if (o.metricIntervalMs !== undefined) c.metricIntervalMs = o.metricIntervalMs;
  if (o.shutdownTimeoutMs !== undefined) c.shutdownTimeoutMs = o.shutdownTimeoutMs;
  if (o.exportTimeoutMs !== undefined) c.exportTimeoutMs = o.exportTimeoutMs;
  if (o.hostRoot !== undefined) c.hostRoot = o.hostRoot;
  if (o.infraStateDir !== undefined) c.infraStateDir = o.infraStateDir;
  if (o.infraRuntimeDir !== undefined) c.infraRuntimeDir = o.infraRuntimeDir;
  if (o.stateDir !== undefined) c.stateDir = o.stateDir;
  if (o.dbQueryText !== undefined) c.dbQueryText = o.dbQueryText;
  if (o.logsConsole !== undefined) c.logsConsole = o.logsConsole;
  if (o.logsExport !== undefined) c.logsExport = o.logsExport;
  if (o.disabledInstrumentations !== undefined) c.disabledInstrumentations = [...o.disabledInstrumentations];
  if (o.instrumentationConfig !== undefined) c.instrumentationConfig = { ...o.instrumentationConfig };
  if (o.httpIgnorePaths !== undefined) c.httpIgnorePaths = [...o.httpIgnorePaths];
  if (o.shutdownOnSignal !== undefined) c.shutdownOnSignal = o.shutdownOnSignal;

  // ---- normalize and validate ----
  switch (protocol.toLowerCase()) {
    case 'http/protobuf':
    case 'http':
    case '':
      c.protocol = PROTOCOL_HTTP;
      break;
    case 'grpc':
      c.protocol = PROTOCOL_GRPC;
      break;
    default:
      throw new ConfigError(`openlog: unsupported protocol ${JSON.stringify(protocol)} (use ${PROTOCOL_HTTP} or ${PROTOCOL_GRPC})`);
  }
  switch (compression.toLowerCase()) {
    case 'gzip':
      c.compression = 'gzip';
      break;
    case 'none':
    case '':
      c.compression = compression === '' ? 'gzip' : 'none';
      break;
    default:
      throw new ConfigError(`openlog: unsupported compression ${JSON.stringify(compression)} (use gzip or none)`);
  }
  if (!c.endpoint) c.endpoint = c.protocol === PROTOCOL_GRPC ? DEFAULT_GRPC_ENDPOINT : DEFAULT_HTTP_ENDPOINT;
  if (!c.endpoint.includes('://')) c.endpoint = 'https://' + c.endpoint;
  let u: URL;
  try {
    u = new URL(c.endpoint);
  } catch {
    throw new ConfigError(`openlog: invalid endpoint ${JSON.stringify(c.endpoint)}`);
  }
  if (!u.host || (u.protocol !== 'http:' && u.protocol !== 'https:')) {
    throw new ConfigError(`openlog: invalid endpoint ${JSON.stringify(c.endpoint)}`);
  }
  c.endpoint = c.endpoint.replace(/\/+$/, '');
  if (Number.isNaN(c.samplingRatio) || c.samplingRatio < 0 || c.samplingRatio > 1) {
    warn(`sampling ratio ${c.samplingRatio} outside 0..1; clamped`);
    c.samplingRatio = Number.isNaN(c.samplingRatio) ? 1 : Math.min(Math.max(c.samplingRatio, 0), 1);
  }
  if (!c.serviceName) {
    const fromAttrs = c.resourceAttributes['service.name'];
    if (fromAttrs) {
      c.serviceName = fromAttrs;
    } else {
      const script = process.argv[1] ? path.basename(process.argv[1]).replace(/\.[cm]?[jt]s$/, '') : '';
      c.serviceName = `unknown_service:${env.npm_package_name || script || 'node'}`;
      warn(`no service name configured (OPENLOG_SERVICE_NAME); using ${JSON.stringify(c.serviceName)}`);
    }
  }
  if (c.licenseKey) {
    c.headers[LICENSE_KEY_HEADER] = c.licenseKey;
  } else if (!(LICENSE_KEY_HEADER in c.headers) && !c.headers['Authorization'] && !c.headers['authorization']) {
    warn('no license key configured (OPENLOG_LICENSE_KEY); openlog ingest will reject the data');
  }
  if (!(c.metricIntervalMs > 0)) c.metricIntervalMs = 60_000;
  if (!(c.shutdownTimeoutMs > 0)) c.shutdownTimeoutMs = 5_000;
  if (!(c.exportTimeoutMs > 0)) c.exportTimeoutMs = 10_000;
  if (!c.hostRoot) c.hostRoot = '/';
  return { config: c, warnings };
}
