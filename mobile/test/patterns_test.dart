// Log patterns: the same question as the list, asked differently.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/fields.dart';
import 'package:openlog_mobile/src/logs.dart';
import 'package:openlog_mobile/src/ui/log_patterns_body.dart';

import 'fake_server.dart';

Map<String, Object?> pattern(String id, int count) => {
  'pattern_id': id,
  'template': 'user <*> logged in from <*>',
  'count': count,
  'severity': {
    'unspecified': 0,
    'trace': 0,
    'debug': 0,
    'info': count - 2,
    'warn': 1,
    'error': 1,
    'fatal': 0,
  },
  'max_severity_number': 17,
  'services': ['checkout'],
  'first_seen': '2026-10-09T09:00:00.000000000Z',
  'last_seen': '2026-10-10T01:00:00.000000000Z',
  'sample': {
    'timestamp': '2026-10-10T01:00:00.000000000Z',
    'body': 'user 42 logged in from 10.0.0.9',
    'service_name': 'checkout',
    'severity_text': 'INFO',
    'severity_number': 9,
    'trace_id': '',
  },
};

void main() {
  test('the search box and the filters go with the patterns', () async {
    Map<String, Object?>? body;
    final server = await FakeServer.start((req, seen) {
      body = jsonDecode(seen.body) as Map<String, Object?>;
      writeJson(req, 200, {
        'patterns': [pattern('p1', 120)],
        'total': 900,
        'unclassified': 12,
        'rollup': true,
        'truncated': false,
      });
    });
    addTearDown(server.stop);
    final c =
        LogPatternsController(
            OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
          )
          ..query = 'timeout'
          ..filters = const [
            Filter(key: 'service.name', op: '=', values: ['checkout']),
          ];
    addTearDown(c.dispose);

    await c.refresh();

    expect(body?['q'], 'timeout');
    expect((body?['filters']! as List).single, {
      'key': 'service.name',
      'op': '=',
      'value': 'checkout',
    });
    // The counts the screen prints beside the list: records with no
    // pattern are part of the answer, and a rollup covers whole hours.
    expect(c.total, 900);
    expect(c.unclassified, 12);
    expect(c.rollup, isTrue);
  });

  test('one pattern is listed by its id, which is a filter key', () {
    final p = LogPattern.fromJson(pattern('774411', 5));
    final f = patternFilter(p);

    expect(f.toJson(), {'key': 'pattern_id', 'op': '=', 'value': '774411'});
  });
}
