import { DiagLogLevel, type DiagLogger } from '@opentelemetry/api';

export type LogLevel = 'debug' | 'info' | 'warn' | 'error' | 'off';

const ORDER: Record<LogLevel, number> = { debug: 0, info: 1, warn: 2, error: 3, off: 100 };

export function parseLogLevel(v: string): LogLevel | undefined {
  switch (v.trim().toLowerCase()) {
    case 'debug':
    case 'verbose':
    case 'all':
      return 'debug';
    case 'info':
      return 'info';
    case 'warn':
    case 'warning':
      return 'warn';
    case 'error':
      return 'error';
    case 'off':
    case 'none':
      return 'off';
  }
  return undefined;
}

export function toDiagLogLevel(l: LogLevel): DiagLogLevel {
  return { debug: DiagLogLevel.DEBUG, info: DiagLogLevel.INFO, warn: DiagLogLevel.WARN, error: DiagLogLevel.ERROR, off: DiagLogLevel.NONE }[l];
}

export type Write = (line: string) => void;

const stderrWrite: Write = (line) => {
  process.stderr.write(line + '\n');
};

function fmt(v: unknown): string {
  if (v instanceof Error) return JSON.stringify(v.message);
  if (typeof v === 'string') return /[\s"=]/.test(v) ? JSON.stringify(v) : v;
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}

/** The agent's own diagnostics (logfmt-like lines on stderr, like the Go agent's slog text handler). */
export class Diag {
  constructor(
    readonly level: LogLevel,
    private readonly write: Write = stderrWrite,
  ) {}

  enabled(l: LogLevel): boolean {
    return ORDER[l] >= ORDER[this.level] && this.level !== 'off';
  }

  log(l: LogLevel, msg: string, fields: Record<string, unknown> = {}): void {
    if (!this.enabled(l)) return;
    let line = `time=${new Date().toISOString()} level=${l.toUpperCase()} msg=${fmt(msg)} component=openlog-node-agent`;
    for (const [k, v] of Object.entries(fields)) {
      if (v !== undefined) line += ` ${k}=${fmt(v)}`;
    }
    this.write(line);
  }

  debug(msg: string, f?: Record<string, unknown>): void {
    this.log('debug', msg, f);
  }
  info(msg: string, f?: Record<string, unknown>): void {
    this.log('info', msg, f);
  }
  warn(msg: string, f?: Record<string, unknown>): void {
    this.log('warn', msg, f);
  }
  error(msg: string, f?: Record<string, unknown>): void {
    this.log('error', msg, f);
  }
}

/**
 * Receives OpenTelemetry diagnostics (diag.setLogger) and SDK/exporter errors. Export failures are never fatal to
 * the application: warnings and errors are logged at most once per interval per message, with the number of
 * suppressed repeats (same policy as the Go agent's error handler).
 */
export class RateLimitedDiagLogger implements DiagLogger {
  private readonly seen = new Map<string, { last: number; suppressed: number }>();

  constructor(
    private readonly diag: Diag,
    private readonly intervalMs = 60_000,
    private readonly now: () => number = Date.now,
  ) {}

  private limited(level: LogLevel, message: string, args: unknown[]): void {
    if (!this.diag.enabled(level)) return;
    let msg = [message, ...args.map((a) => (a instanceof Error ? a.message : typeof a === 'string' ? a : fmt(a)))].join(' ');
    if (msg.length > 512) msg = msg.slice(0, 512);
    const now = this.now();
    const st = this.seen.get(msg);
    if (st && now - st.last < this.intervalMs) {
      st.suppressed++;
      return;
    }
    if (this.seen.size > 256) this.seen.clear();
    this.seen.set(msg, { last: now, suppressed: 0 });
    this.diag.log(level, level === 'error' || level === 'warn' ? 'telemetry error' : 'opentelemetry', {
      error: msg,
      suppressed_repeats: st ? st.suppressed : undefined,
    });
  }

  error(message: string, ...args: unknown[]): void {
    this.limited('error', message, args);
  }
  warn(message: string, ...args: unknown[]): void {
    this.limited('warn', message, args);
  }
  info(message: string, ...args: unknown[]): void {
    if (this.diag.enabled('info')) this.diag.info(message, args.length ? { detail: args.map(String).join(' ') } : {});
  }
  debug(message: string, ...args: unknown[]): void {
    if (this.diag.enabled('debug')) this.diag.debug(message, args.length ? { detail: args.map((a) => (typeof a === 'string' ? a : fmt(a))).join(' ') } : {});
  }
  verbose(message: string, ...args: unknown[]): void {
    this.debug(message, ...args);
  }
}
