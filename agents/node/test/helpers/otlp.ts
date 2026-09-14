// OTLP/HTTP capture server for tests: decodes protobuf ExportTraceServiceRequest, ExportMetricsServiceRequest and
// ExportLogsServiceRequest (gzip or plain) with a minimal hand-written decoder of the opentelemetry-proto fields the
// tests assert on (no protobuf dependency).
import * as http from 'node:http';
import type { AddressInfo } from 'node:net';
import { gunzipSync } from 'node:zlib';

// ---------------------------------------------------------------- protobuf wire format

class Reader {
  pos = 0;
  constructor(readonly buf: Buffer) {}
  done(): boolean {
    return this.pos >= this.buf.length;
  }
  varint(): bigint {
    let result = 0n;
    let shift = 0n;
    for (;;) {
      const b = this.buf[this.pos++];
      result |= BigInt(b & 0x7f) << shift;
      if ((b & 0x80) === 0) return result;
      shift += 7n;
    }
  }
  tag(): [number, number] {
    const t = Number(this.varint());
    return [t >>> 3, t & 7];
  }
  bytes(): Buffer {
    const len = Number(this.varint());
    const b = this.buf.subarray(this.pos, this.pos + len);
    this.pos += len;
    return b;
  }
  fixed64(): bigint {
    const v = this.buf.readBigUInt64LE(this.pos);
    this.pos += 8;
    return v;
  }
  fixed32(): number {
    const v = this.buf.readUInt32LE(this.pos);
    this.pos += 4;
    return v;
  }
  double(): number {
    const v = this.buf.readDoubleLE(this.pos);
    this.pos += 8;
    return v;
  }
  skip(wire: number): void {
    if (wire === 0) this.varint();
    else if (wire === 1) this.pos += 8;
    else if (wire === 2) this.bytes();
    else if (wire === 5) this.pos += 4;
    else throw new Error(`unsupported wire type ${wire}`);
  }
}

type Handler = (r: Reader, wire: number) => void;

function decode(buf: Buffer, fields: Record<number, Handler>): void {
  const r = new Reader(buf);
  while (!r.done()) {
    const [f, wire] = r.tag();
    const h = fields[f];
    if (h) h(r, wire);
    else r.skip(wire);
  }
}

export type AnyValue = string | boolean | number | bigint | AnyValue[] | { [k: string]: AnyValue } | Buffer | null;
export type Attrs = Record<string, AnyValue>;

function anyValue(buf: Buffer): AnyValue {
  let v: AnyValue = null;
  decode(buf, {
    1: (r) => (v = r.bytes().toString('utf8')),
    2: (r) => (v = r.varint() !== 0n),
    3: (r) => (v = Number(BigInt.asIntN(64, r.varint()))),
    4: (r) => (v = r.double()),
    5: (r) => {
      const arr: AnyValue[] = [];
      decode(r.bytes(), { 1: (rr) => arr.push(anyValue(rr.bytes())) });
      v = arr;
    },
    6: (r) => (v = keyValues(r.bytes(), 1)),
    7: (r) => (v = Buffer.from(r.bytes())),
  });
  return v;
}

function keyValue(buf: Buffer): [string, AnyValue] {
  let k = '';
  let val: AnyValue = null;
  decode(buf, { 1: (r) => (k = r.bytes().toString('utf8')), 2: (r) => (val = anyValue(r.bytes())) });
  return [k, val];
}

/** Decodes the repeated KeyValue field `field` of a message. */
function keyValues(buf: Buffer, field: number): Attrs {
  const out: Attrs = {};
  decode(buf, {
    [field]: (r) => {
      const [k, v] = keyValue(r.bytes());
      out[k] = v;
    },
  });
  return out;
}

export interface Scope {
  name: string;
  version: string;
}

function scope(buf: Buffer): Scope {
  const s = { name: '', version: '' };
  decode(buf, { 1: (r) => (s.name = r.bytes().toString('utf8')), 2: (r) => (s.version = r.bytes().toString('utf8')) });
  return s;
}

// ---------------------------------------------------------------- traces

export interface CapturedSpan {
  traceId: string;
  spanId: string;
  parentSpanId: string;
  traceState: string;
  name: string;
  kind: number; // 1 internal, 2 server, 3 client, 4 producer, 5 consumer
  flags: number;
  startNs: bigint;
  endNs: bigint;
  attributes: Attrs;
  events: { name: string; attributes: Attrs }[];
  status: { code: number; message: string };
  resource: Attrs;
  scope: Scope;
}

function span(buf: Buffer, resource: Attrs, sc: Scope): CapturedSpan {
  const s: CapturedSpan = {
    traceId: '',
    spanId: '',
    parentSpanId: '',
    traceState: '',
    name: '',
    kind: 0,
    flags: 0,
    startNs: 0n,
    endNs: 0n,
    attributes: {},
    events: [],
    status: { code: 0, message: '' },
    resource,
    scope: sc,
  };
  decode(buf, {
    1: (r) => (s.traceId = r.bytes().toString('hex')),
    2: (r) => (s.spanId = r.bytes().toString('hex')),
    3: (r) => (s.traceState = r.bytes().toString('utf8')),
    4: (r) => (s.parentSpanId = r.bytes().toString('hex')),
    5: (r) => (s.name = r.bytes().toString('utf8')),
    6: (r) => (s.kind = Number(r.varint())),
    7: (r) => (s.startNs = r.fixed64()),
    8: (r) => (s.endNs = r.fixed64()),
    9: (r) => {
      const [k, v] = keyValue(r.bytes());
      s.attributes[k] = v;
    },
    11: (r) => {
      const ev = { name: '', attributes: {} as Attrs };
      decode(r.bytes(), {
        2: (rr) => (ev.name = rr.bytes().toString('utf8')),
        3: (rr) => {
          const [k, v] = keyValue(rr.bytes());
          ev.attributes[k] = v;
        },
      });
      s.events.push(ev);
    },
    15: (r) => decode(r.bytes(), { 2: (rr) => (s.status.message = rr.bytes().toString('utf8')), 3: (rr) => (s.status.code = Number(rr.varint())) }),
    16: (r) => (s.flags = r.fixed32()),
  });
  return s;
}

export function decodeTraces(buf: Buffer): CapturedSpan[] {
  const out: CapturedSpan[] = [];
  decode(buf, {
    1: (r) => {
      const rs = r.bytes();
      let res: Attrs = {};
      decode(rs, { 1: (rr) => (res = keyValues(rr.bytes(), 1)) });
      decode(rs, {
        2: (rr) => {
          const ss = rr.bytes();
          let sc: Scope = { name: '', version: '' };
          decode(ss, { 1: (x) => (sc = scope(x.bytes())) });
          decode(ss, { 2: (x) => out.push(span(x.bytes(), res, sc)) });
        },
      });
    },
  });
  return out;
}

// ---------------------------------------------------------------- metrics

export interface CapturedPoint {
  attributes: Attrs;
  value?: number;
  count?: number;
  sum?: number;
  bucketCounts?: number[];
  bounds?: number[];
}

export interface CapturedMetric {
  name: string;
  unit: string;
  type: 'gauge' | 'sum' | 'histogram' | 'exponential_histogram' | 'summary' | 'unknown';
  monotonic?: boolean;
  temporality?: number;
  points: CapturedPoint[];
  resource: Attrs;
  scope: Scope;
}

function numberPoint(buf: Buffer): CapturedPoint {
  const p: CapturedPoint = { attributes: {} };
  decode(buf, {
    4: (r) => (p.value = r.double()),
    6: (r) => (p.value = Number(BigInt.asIntN(64, r.fixed64()))),
    7: (r) => {
      const [k, v] = keyValue(r.bytes());
      p.attributes[k] = v;
    },
  });
  return p;
}

function histogramPoint(buf: Buffer): CapturedPoint {
  const p: CapturedPoint = { attributes: {}, bucketCounts: [], bounds: [] };
  decode(buf, {
    4: (r) => (p.count = Number(r.fixed64())),
    5: (r) => (p.sum = r.double()),
    6: (r, wire) => {
      if (wire === 2) {
        const b = r.bytes();
        for (let i = 0; i + 8 <= b.length; i += 8) p.bucketCounts!.push(Number(b.readBigUInt64LE(i)));
      } else p.bucketCounts!.push(Number(r.fixed64()));
    },
    7: (r, wire) => {
      if (wire === 2) {
        const b = r.bytes();
        for (let i = 0; i + 8 <= b.length; i += 8) p.bounds!.push(b.readDoubleLE(i));
      } else p.bounds!.push(r.double());
    },
    9: (r) => {
      const [k, v] = keyValue(r.bytes());
      p.attributes[k] = v;
    },
  });
  return p;
}

function metric(buf: Buffer, resource: Attrs, sc: Scope): CapturedMetric {
  const m: CapturedMetric = { name: '', unit: '', type: 'unknown', points: [], resource, scope: sc };
  decode(buf, {
    1: (r) => (m.name = r.bytes().toString('utf8')),
    3: (r) => (m.unit = r.bytes().toString('utf8')),
    5: (r) => {
      m.type = 'gauge';
      decode(r.bytes(), { 1: (x) => m.points.push(numberPoint(x.bytes())) });
    },
    7: (r) => {
      m.type = 'sum';
      decode(r.bytes(), {
        1: (x) => m.points.push(numberPoint(x.bytes())),
        2: (x) => (m.temporality = Number(x.varint())),
        3: (x) => (m.monotonic = x.varint() !== 0n),
      });
    },
    9: (r) => {
      m.type = 'histogram';
      decode(r.bytes(), { 1: (x) => m.points.push(histogramPoint(x.bytes())), 2: (x) => (m.temporality = Number(x.varint())) });
    },
    10: (r) => {
      m.type = 'exponential_histogram';
      r.bytes();
    },
    11: (r) => {
      m.type = 'summary';
      r.bytes();
    },
  });
  return m;
}

export function decodeMetrics(buf: Buffer): CapturedMetric[] {
  const out: CapturedMetric[] = [];
  decode(buf, {
    1: (r) => {
      const rm = r.bytes();
      let res: Attrs = {};
      decode(rm, { 1: (rr) => (res = keyValues(rr.bytes(), 1)) });
      decode(rm, {
        2: (rr) => {
          const sm = rr.bytes();
          let sc: Scope = { name: '', version: '' };
          decode(sm, { 1: (x) => (sc = scope(x.bytes())) });
          decode(sm, { 2: (x) => out.push(metric(x.bytes(), res, sc)) });
        },
      });
    },
  });
  return out;
}

// ---------------------------------------------------------------- logs

export interface CapturedLog {
  severityNumber: number;
  severityText: string;
  body: AnyValue;
  attributes: Attrs;
  traceId: string;
  spanId: string;
  resource: Attrs;
  scope: Scope;
}

function logRecord(buf: Buffer, resource: Attrs, sc: Scope): CapturedLog {
  const l: CapturedLog = { severityNumber: 0, severityText: '', body: null, attributes: {}, traceId: '', spanId: '', resource, scope: sc };
  decode(buf, {
    2: (r) => (l.severityNumber = Number(r.varint())),
    3: (r) => (l.severityText = r.bytes().toString('utf8')),
    5: (r) => (l.body = anyValue(r.bytes())),
    6: (r) => {
      const [k, v] = keyValue(r.bytes());
      l.attributes[k] = v;
    },
    9: (r) => (l.traceId = r.bytes().toString('hex')),
    10: (r) => (l.spanId = r.bytes().toString('hex')),
  });
  return l;
}

export function decodeLogs(buf: Buffer): CapturedLog[] {
  const out: CapturedLog[] = [];
  decode(buf, {
    1: (r) => {
      const rl = r.bytes();
      let res: Attrs = {};
      decode(rl, { 1: (rr) => (res = keyValues(rr.bytes(), 1)) });
      decode(rl, {
        2: (rr) => {
          const sl = rr.bytes();
          let sc: Scope = { name: '', version: '' };
          decode(sl, { 1: (x) => (sc = scope(x.bytes())) });
          decode(sl, { 2: (x) => out.push(logRecord(x.bytes(), res, sc)) });
        },
      });
    },
  });
  return out;
}

// ---------------------------------------------------------------- server

export interface CapturedRequest {
  path: string;
  headers: http.IncomingHttpHeaders;
}

export interface Capture {
  url: string;
  requests: CapturedRequest[];
  spans: CapturedSpan[];
  metrics: CapturedMetric[];
  logs: CapturedLog[];
  /** Set to make the server answer every request with this status (retry/outage tests). */
  failWith?: number;
  waitFor<T>(what: string, fn: () => T | undefined | false, timeoutMs?: number): Promise<T>;
  close(): Promise<void>;
}

export async function startCapture(): Promise<Capture> {
  const cap: Capture = {
    url: '',
    requests: [],
    spans: [],
    metrics: [],
    logs: [],
    async waitFor<T>(what: string, fn: () => T | undefined | false, timeoutMs = 15_000): Promise<T> {
      const deadline = Date.now() + timeoutMs;
      for (;;) {
        const v = fn();
        if (v !== undefined && v !== false) return v as T;
        if (Date.now() > deadline) {
          throw new Error(
            `timed out waiting for ${what}; captured spans: ${cap.spans.map((s) => `${s.name}[${s.kind}]`).join(', ')}; ` +
              `metrics: ${[...new Set(cap.metrics.map((m) => m.name))].join(', ')}; logs: ${cap.logs.length}`,
          );
        }
        await new Promise((r) => setTimeout(r, 50));
      }
    },
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (c: Buffer) => chunks.push(c));
    req.on('end', () => {
      let body = Buffer.concat(chunks);
      cap.requests.push({ path: req.url ?? '', headers: req.headers });
      if (cap.failWith) {
        res.writeHead(cap.failWith, { 'content-type': 'text/plain', 'retry-after': '0' });
        res.end('unavailable');
        return;
      }
      try {
        if (req.headers['content-encoding'] === 'gzip') body = gunzipSync(body);
        if (req.url === '/v1/traces') cap.spans.push(...decodeTraces(body));
        else if (req.url === '/v1/metrics') cap.metrics.push(...decodeMetrics(body));
        else if (req.url === '/v1/logs') cap.logs.push(...decodeLogs(body));
      } catch (err) {
        res.writeHead(400);
        res.end(String(err));
        return;
      }
      res.writeHead(200, { 'content-type': 'application/x-protobuf' });
      res.end();
    });
  });
  server.keepAliveTimeout = 1000;
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  cap.url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  return cap;
}
