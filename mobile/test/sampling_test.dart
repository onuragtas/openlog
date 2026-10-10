// The sampling policy: what reaches the server, and when.
//
// The rule that matters is that nothing reaches it until the save button. A
// policy applied halfway is a policy nobody chose: turning the rate limit
// down before adding the rule that keeps the errors throws away the errors
// in between.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/sampling.dart';

import 'fake_server.dart';

Map<String, Object?> state({
  bool enabled = true,
  int version = 3,
  bool isDefault = false,
  List<Map<String, Object?>> rules = const [],
}) => {
  'enabled': enabled,
  'is_default': isDefault,
  'version': version,
  'updated_at': '2026-10-09T10:00:00.000000000Z',
  'updated_by_email': 'owner@example.com',
  'policy': {
    'enabled': true,
    'baseline_ratio': 0.1,
    'max_spans_per_second': 0,
    'rules': rules,
  },
};

Map<String, Object?> rule(String name, String type, double ratio) => {
  'name': name,
  'type': type,
  'ratio': ratio,
};

void main() {
  test('editing changes nothing on the server until save', () async {
    final sent = <(String, Object?)>[];
    final server = await FakeServer.start((req, seen) {
      sent.add((seen.method, seen.body.isEmpty ? null : jsonDecode(seen.body)));
      writeJson(req, 200, state(rules: [rule('errors', 'error', 1)]));
    });
    addTearDown(server.stop);
    final c = SamplingController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    expect(c.dirty, isFalse);

    c.edit(withRules(c.draft!, const []));
    expect(c.dirty, isTrue);
    // Still only the GET: a policy halfway applied is one nobody chose.
    expect(sent.map((s) => s.$1), ['GET']);

    await c.save();
    // The PUT answers with the stored policy, so there is no reload after
    // it: asking again would only risk showing something older.
    expect(sent.map((s) => s.$1), ['GET', 'PUT']);
    final body = sent[1].$2! as Map<String, Object?>;
    // The version that was edited, so a save on top of somebody else's is
    // a 409 rather than a silent overwrite.
    expect(body['version'], 3);
    expect((body['policy']! as Map)['rules'], isEmpty);
  });

  test('a 409 keeps the draft and says to reload', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'PUT') {
        writeJson(req, 409, {
          'error': {'message': 'stale version'},
        });
        return;
      }
      writeJson(req, 200, state(rules: [rule('errors', 'error', 1)]));
    });
    addTearDown(server.stop);
    final c = SamplingController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    c.edit(withRules(c.draft!, const []));
    await c.save();

    // The draft is work somebody did; losing it to a reload would be worse
    // than the conflict.
    expect(c.draft!.rules, isEmpty);
    expect(c.dirty, isTrue);
    expect(c.failure?.kind, 'samplingConflict');
  });

  test('an estimate belongs to the draft it was made of', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 200, {
          'window_minutes': 60,
          'traces_examined': 1200,
          'sampled_fraction': 1.0,
          'estimated_traces': 1200.0,
          'kept_trace_ratio': 0.42,
          'kept_span_ratio': 0.37,
          'rules': [
            {
              'name': 'errors',
              'matched_trace_ratio': 0.08,
              'kept_trace_ratio': 0.08,
            },
          ],
        });
        return;
      }
      writeJson(req, 200, state(rules: [rule('errors', 'error', 1)]));
    });
    addTearDown(server.stop);
    final c = SamplingController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );
    addTearDown(c.dispose);

    await c.load();
    await c.estimate();
    expect(c.preview?.keptTraceRatio, 0.42);

    // Changing the policy drops the estimate rather than leaving a number
    // that was computed for something else looking current.
    c.edit(withRules(c.draft!, const []));
    expect(c.preview, isNull);
  });

  test('moving a rule changes what the later ones see', () {
    final rules = [
      TailSamplingRule.fromJson(rule('errors', 'error', 1)),
      TailSamplingRule.fromJson(rule('slow', 'latency', 0.5)),
      TailSamplingRule.fromJson(rule('rest', 'service', 0.1)),
    ];

    // The first matching rule decides, so the order is the policy.
    expect(movedRules(rules, 2, 0).map((r) => r.name), [
      'rest',
      'errors',
      'slow',
    ]);
    expect(movedRules(rules, 0, 3).map((r) => r.name), [
      'slow',
      'rest',
      'errors',
    ]);
  });
}
