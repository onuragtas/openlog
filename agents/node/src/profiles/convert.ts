// V8 CPU profile -> OTLP profiles (docs/contracts/profiles.md).
//
// Written by hand, as OTLP/JSON, for two reasons. The OpenTelemetry JS SDK has no profiles exporter —
// profiles are not part of its stable surface — so there is nothing to reuse; and the ingest accepts JSON
// and re-encodes it to protobuf itself, so the agent needs no protobuf runtime to speak the wire format.
//
// The field names below are the proto's own (snake_case), which protojson accepts, and int64 fields are
// strings: a JSON number cannot carry them without losing digits past 2^53, and a duration in nanoseconds
// passes that in under two hours.
//
// **Frames are function names only.** The server's stack model is a list of names (internal/profiles
// resolveStack reads FunctionTable.name_strindex), so file and line are deliberately not encoded: they
// would be interned, sent, and then dropped.

/** The shape `inspector`'s Profiler.takeProfile returns (Profiler.Profile). */
export interface V8Profile {
  nodes: V8Node[];
  /** Microseconds since an arbitrary epoch. */
  startTime: number;
  endTime: number;
  /** Sampled node ids, and the microseconds between consecutive samples. */
  samples?: number[];
  timeDeltas?: number[];
}

export interface V8Node {
  id: number;
  callFrame: { functionName?: string; url?: string; lineNumber?: number; columnNumber?: number };
  hitCount?: number;
  children?: number[];
}

export interface KeyValue {
  key: string;
  value: { stringValue: string };
}

/** Caps, mirroring the Go agent so one producer cannot be an order of magnitude heavier than another. */
export const MAX_SAMPLES = 20_000;
export const MAX_FRAME_BYTES = 512;

interface Dict {
  string_table: string[];
  function_table: { name_strindex: number }[];
  location_table: { lines: { function_index: number }[] }[];
  stack_table: { location_indices: number[] }[];
}

class Builder {
  readonly dict: Dict = { string_table: [], function_table: [], location_table: [], stack_table: [] };
  private readonly strings = new Map<string, number>();
  private readonly functions = new Map<string, number>();
  private readonly locations = new Map<number, number>();
  private readonly stacks = new Map<string, number>();

  str(s: string): number {
    const found = this.strings.get(s);
    if (found !== undefined) return found;
    const i = this.dict.string_table.length;
    this.dict.string_table.push(s);
    this.strings.set(s, i);
    return i;
  }

  /** One location per V8 node, one function per name: the dictionary is interned, not repeated. */
  location(nodeId: number, name: string): number {
    const found = this.locations.get(nodeId);
    if (found !== undefined) return found;
    let fn = this.functions.get(name);
    if (fn === undefined) {
      fn = this.dict.function_table.length;
      this.dict.function_table.push({ name_strindex: this.str(name.slice(0, MAX_FRAME_BYTES)) });
      this.functions.set(name, fn);
    }
    const i = this.dict.location_table.length;
    this.dict.location_table.push({ lines: [{ function_index: fn }] });
    this.locations.set(nodeId, i);
    return i;
  }

  /** `indices` must already be leaf first: OTLP orders a stack that way, and the server reverses it. */
  stack(indices: number[]): number {
    const key = indices.join(',');
    const found = this.stacks.get(key);
    if (found !== undefined) return found;
    const i = this.dict.stack_table.length;
    this.dict.stack_table.push({ location_indices: indices });
    this.stacks.set(key, i);
    return i;
  }
}

/** The name a frame is stored under. V8 leaves the main script and native frames unnamed. */
function frameName(n: V8Node): string {
  const fn = n.callFrame.functionName;
  return fn && fn.length > 0 ? fn : '(anonymous)';
}

/**
 * Converts one V8 CPU profile into an OTLP ProfilesData object, or null when the process was idle.
 *
 * Null rather than an empty payload on purpose: the ingest accepts a profile-less body and produces
 * nothing, so sending one would be a request that costs both sides and says nothing.
 */
export function fromV8Profile(
  p: V8Profile,
  resource: KeyValue[],
  scope: { name: string; version: string },
  endWallMs: number,
): Record<string, unknown> | null {
  if (!p || !Array.isArray(p.nodes) || p.nodes.length === 0) return null;
  const samples = p.samples ?? [];
  const deltas = p.timeDeltas ?? [];
  if (samples.length === 0) return null;

  const byId = new Map<number, V8Node>();
  for (const n of p.nodes) byId.set(n.id, n);
  // V8 gives children; a stack needs parents.
  const parent = new Map<number, number>();
  for (const n of p.nodes) for (const c of n.children ?? []) parent.set(c, n.id);

  // Nanoseconds per sampled node: a sample's delta is the time since the previous one, in microseconds.
  const nanos = new Map<number, number>();
  for (let i = 0; i < samples.length; i++) {
    const d = deltas[i];
    // The only place a zero is dropped: a sample worth no time contributes nothing to any aggregate and
    // would still cost a row downstream. Everything in `nanos` below is therefore strictly positive.
    if (typeof d !== 'number' || d <= 0) continue;
    nanos.set(samples[i], (nanos.get(samples[i]) ?? 0) + d * 1000);
  }

  const b = new Builder();
  const out: { stack_index: number; values: string[] }[] = [];
  for (const [nodeId, value] of nanos) {
    if (out.length >= MAX_SAMPLES) break;
    const indices: number[] = [];
    for (let id: number | undefined = nodeId; id !== undefined; id = parent.get(id)) {
      const n = byId.get(id);
      if (!n) break;
      indices.push(b.location(n.id, frameName(n))); // leaf first, walking up to the root
    }
    if (indices.length === 0) continue;
    out.push({ stack_index: b.stack(indices), values: [String(value)] });
  }
  if (out.length === 0) return null;

  const durationNs = Math.max(0, Math.round((p.endTime - p.startTime) * 1000));
  return {
    dictionary: b.dict,
    resource_profiles: [
      {
        resource: { attributes: resource },
        scope_profiles: [
          {
            scope: { name: scope.name, version: scope.version },
            profiles: [
              {
                // The server refuses a profile whose sample type resolves to an empty string: a chart of
                // unnamed numbers is worse than no chart.
                sample_type: { type_strindex: b.str('cpu'), unit_strindex: b.str('nanoseconds') },
                samples: out,
                time_unix_nano: String(Math.round(endWallMs) * 1_000_000),
                duration_nano: String(durationNs),
              },
            ],
          },
        ],
      },
    ],
  };
}
