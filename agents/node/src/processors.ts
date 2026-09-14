import { SpanKind, type Context } from '@opentelemetry/api';
import type { ReadableSpan, Span, SpanProcessor } from '@opentelemetry/sdk-trace-base';
import type { DbQueryTextMode } from './config';
import { sanitizeKeyValue, sanitizeSQL, truncateQueryText } from './sanitize';

const QUERY_TEXT_KEYS = ['db.query.text', 'db.statement'] as const;
const KV_SYSTEMS = new Set(['redis', 'valkey', 'memcached']);
// Document/search stores whose instrumentations already mask values (e.g. MongoDB `{"_id":"?"}`) or use JSON bodies.
const NON_SQL_SYSTEMS = new Set(['mongodb', 'elasticsearch', 'opensearch', 'aws.dynamodb', 'dynamodb', 'couchdb', 'couchbase', 'azure.cosmosdb', 'cosmosdb']);

type MutableAttributes = Record<string, unknown>;

/**
 * Applies OPENLOG_DB_QUERY_TEXT to every span's db.query.text / db.statement right before the span ends
 * (instrumentations such as pg set the text after the span started): `sanitized` normalizes SQL like the Go agent and
 * key/value commands to `CMD ? ?`, `raw` keeps the text, `off` removes it. Statements are capped at 4096 characters.
 */
export class DbStatementProcessor implements SpanProcessor {
  constructor(private readonly mode: DbQueryTextMode) {}

  onStart(): void {}

  onEnding(span: Span): void {
    const attrs = span.attributes as MutableAttributes;
    for (const key of QUERY_TEXT_KEYS) {
      const text = attrs[key];
      if (typeof text !== 'string') continue;
      if (this.mode === 'off') {
        delete attrs[key];
        continue;
      }
      let out = text;
      if (this.mode === 'sanitized') {
        const system = String(attrs['db.system.name'] ?? attrs['db.system'] ?? '').toLowerCase();
        if (KV_SYSTEMS.has(system)) {
          if (!/^[A-Z][A-Z._-]*( \?)*( …)?$/.test(text)) {
            const tokens = text.trim().split(/\s+/);
            out = sanitizeKeyValue(tokens[0] ?? '', tokens.slice(1));
          }
        } else if (!NON_SQL_SYSTEMS.has(system)) {
          out = sanitizeSQL(text, system);
        }
      }
      out = truncateQueryText(out);
      if (out !== text) attrs[key] = out;
    }
  }

  onEnd(): void {}

  forceFlush(): Promise<void> {
    return Promise.resolve();
  }

  shutdown(): Promise<void> {
    return Promise.resolve();
  }
}

interface Entry {
  span: Span;
  route?: string;
}

const MAX_TRACKED = 10_000;

/**
 * Makes APM transactions group by route (apm.md §2.1 uses http.route of the entry span): framework instrumentations
 * (express, koa, @fastify/otel) pass the matched route to the HTTP server span through RPC metadata; others (NestJS
 * on a non-express platform, custom routers, GraphQL-over-HTTP handlers) only set http.route on their own spans. This
 * processor remembers the longest http.route seen on any local descendant of an entry SERVER span and, when the entry
 * span has none, sets http.route and renames it `<METHOD> <route>` before it ends.
 */
export class RouteProcessor implements SpanProcessor {
  private readonly entries = new Map<string, Entry>(); // entry span id → entry
  private readonly owner = new Map<string, Entry>(); // live local span id → its entry

  onStart(span: Span, _parentContext: Context): void {
    const id = span.spanContext().spanId;
    const parent = span.parentSpanContext;
    let entry: Entry | undefined;
    if (span.kind === SpanKind.SERVER && (!parent || parent.isRemote)) {
      entry = { span };
      if (this.entries.size >= MAX_TRACKED) this.entries.clear();
      this.entries.set(id, entry);
    } else if (parent && !parent.isRemote) {
      entry = this.owner.get(parent.spanId);
    }
    if (!entry) return;
    if (this.owner.size >= MAX_TRACKED) this.owner.clear();
    this.owner.set(id, entry);
    this.note(entry, span);
  }

  private note(entry: Entry, span: ReadableSpan): void {
    if (span === entry.span) return;
    const route = span.attributes['http.route'];
    if (typeof route === 'string' && route !== '' && route !== '*' && (!entry.route || route.length > entry.route.length)) {
      entry.route = route;
    }
  }

  onEnding(span: Span): void {
    const id = span.spanContext().spanId;
    const entry = this.owner.get(id);
    if (!entry) return;
    this.note(entry, span);
    if (entry.span !== span) return;
    const current = span.attributes['http.route'];
    if (entry.route && (typeof current !== 'string' || current === '')) {
      span.setAttribute('http.route', entry.route);
      const method = span.attributes['http.request.method'] ?? span.attributes['http.method'];
      if (typeof method === 'string' && method) span.updateName(`${method} ${entry.route}`);
    }
  }

  onEnd(span: ReadableSpan): void {
    const id = span.spanContext().spanId;
    this.owner.delete(id);
    this.entries.delete(id);
  }

  forceFlush(): Promise<void> {
    return Promise.resolve();
  }

  shutdown(): Promise<void> {
    this.entries.clear();
    this.owner.clear();
    return Promise.resolve();
  }
}
