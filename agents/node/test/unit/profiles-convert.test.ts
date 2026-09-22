import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { fromV8Profile, type V8Profile } from '../../src/profiles/convert';

/** root -> middle -> leaf, with the leaf sampled twice and the middle once. */
function profile(overrides: Partial<V8Profile> = {}): V8Profile {
  return {
    nodes: [
      { id: 1, callFrame: { functionName: 'root' }, children: [2] },
      { id: 2, callFrame: { functionName: 'middle' }, children: [3] },
      { id: 3, callFrame: { functionName: 'leaf' } },
    ],
    startTime: 1_000_000,
    endTime: 2_000_000,
    samples: [3, 3, 2],
    timeDeltas: [1000, 1000, 500],
    ...overrides,
  };
}

/** The frame names of a stack, in the order the payload carries them. */
function frames(data: Record<string, unknown>, stackIndex: number): string[] {
  const dict = data.dictionary as {
    string_table: string[];
    function_table: { name_strindex: number }[];
    location_table: { lines: { function_index: number }[] }[];
    stack_table: { location_indices: number[] }[];
  };
  return dict.stack_table[stackIndex].location_indices.map((li) => {
    const fn = dict.location_table[li].lines[0].function_index;
    return dict.string_table[dict.function_table[fn].name_strindex];
  });
}

function samplesOf(data: Record<string, unknown>): { stack_index: number; values: string[] }[] {
  const rp = (data.resource_profiles as Record<string, unknown>[])[0];
  const sp = (rp.scope_profiles as Record<string, unknown>[])[0];
  const prof = (sp.profiles as Record<string, unknown>[])[0];
  return prof.samples as { stack_index: number; values: string[] }[];
}

describe('fromV8Profile', () => {
  it('orders a stack leaf first, the way OTLP does', () => {
    const data = fromV8Profile(profile(), [], { name: 's', version: '1' }, 1_700_000_000_000)!;
    const leafStack = samplesOf(data).find((s) => frames(data, s.stack_index).includes('leaf'))!;
    // The server reverses this to read root first. Emitting it root first would silently invert every
    // flame graph, with nothing failing anywhere.
    assert.deepEqual(frames(data, leafStack.stack_index), ['leaf', 'middle', 'root']);
  });

  it('sums the time of each sampled node in nanoseconds', () => {
    const data = fromV8Profile(profile(), [], { name: 's', version: '1' }, 1_700_000_000_000)!;
    const bySum = Object.fromEntries(
      samplesOf(data).map((s) => [frames(data, s.stack_index)[0], s.values[0]]),
    );
    assert.equal(bySum.leaf, String(2 * 1000 * 1000), 'two 1ms deltas');
    assert.equal(bySum.middle, String(500 * 1000));
  });

  it('names the sample type, which the server refuses to store without', () => {
    const data = fromV8Profile(profile(), [], { name: 's', version: '1' }, 1_700_000_000_000)!;
    const dict = data.dictionary as { string_table: string[] };
    const rp = (data.resource_profiles as Record<string, unknown>[])[0];
    const sp = (rp.scope_profiles as Record<string, unknown>[])[0];
    const prof = (sp.profiles as Record<string, unknown>[])[0];
    const st = prof.sample_type as { type_strindex: number; unit_strindex: number };
    assert.equal(dict.string_table[st.type_strindex], 'cpu');
    assert.equal(dict.string_table[st.unit_strindex], 'nanoseconds');
  });

  it('interns the dictionary instead of repeating it', () => {
    const data = fromV8Profile(profile(), [], { name: 's', version: '1' }, 1_700_000_000_000)!;
    const dict = data.dictionary as { function_table: unknown[]; location_table: unknown[] };
    // Three functions and three nodes, however many samples hit them.
    assert.equal(dict.function_table.length, 3);
    assert.equal(dict.location_table.length, 3);
  });

  it('sends nothing when the process was idle', () => {
    // The ingest accepts a profile-less body and produces nothing, so sending one would cost both sides
    // and say nothing.
    assert.equal(fromV8Profile(profile({ samples: [], timeDeltas: [] }), [], { name: 's', version: '1' }, 0), null);
    assert.equal(fromV8Profile(profile({ timeDeltas: [0, 0, 0] }), [], { name: 's', version: '1' }, 0), null);
  });

  it('carries int64 fields as strings', () => {
    const data = fromV8Profile(profile(), [], { name: 's', version: '1' }, 1_700_000_000_000)!;
    const rp = (data.resource_profiles as Record<string, unknown>[])[0];
    const sp = (rp.scope_profiles as Record<string, unknown>[])[0];
    const prof = (sp.profiles as Record<string, unknown>[])[0];
    // A JSON number loses digits past 2^53, which a duration in nanoseconds passes in under two hours.
    assert.equal(typeof prof.duration_nano, 'string');
    assert.equal(typeof prof.time_unix_nano, 'string');
    assert.equal(prof.duration_nano, String(1_000_000 * 1000));
  });

  it('names the frames V8 leaves anonymous', () => {
    const anon = profile({ nodes: [{ id: 1, callFrame: {} }], samples: [1], timeDeltas: [1000] });
    const data = fromV8Profile(anon, [], { name: 's', version: '1' }, 0)!;
    assert.deepEqual(frames(data, samplesOf(data)[0].stack_index), ['(anonymous)']);
  });
});
