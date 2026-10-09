// Making a rule from a template: the values that go out, and the rule that
// comes back.
//
// The conversions are what is worth testing. A ratio is edited as a
// percentage, so 80 in the box has to leave as 0.8 -- the other way round is
// a rule that fires at 0.8% and nobody notices until it pages all night.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/templates.dart';

import 'fake_server.dart';

Map<String, Object?> param(
  String key, {
  String kind = 'number',
  String? unit,
  bool required = true,
  Object? byDefault,
  double? min,
  double? max,
}) => {
  'key': key,
  'kind': kind,
  'unit': ?unit,
  'required': required,
  'default': byDefault,
  'min': ?min,
  'max': ?max,
  'label': {'en': key, 'tr': key},
};

Map<String, Object?> template(
  String id, {
  List<Map<String, Object?>> params = const [],
  String? referenceMetric,
}) => {
  'id': id,
  'category': 'host',
  'rule_type': 'metric_threshold',
  'severity': 'warning',
  'name': {'en': 'CPU high', 'tr': 'CPU yüksek'},
  'description': {'en': 'cpu', 'tr': 'cpu'},
  'reference_metric': ?referenceMetric,
  'params': params,
};

AlertTemplate parse(Map<String, Object?> json) => AlertTemplate.fromJson(json);

void main() {
  test('a ratio is typed as a percentage and sent as a ratio', () {
    final t = parse(
      template(
        't',
        params: [
          param('threshold', unit: 'ratio', byDefault: 0.8, min: 0.01, max: 1),
        ],
      ),
    );
    final params = editableParams(t);

    // The box opens on 80, not on 0.8.
    expect(initialValues(params), {'threshold': '80'});

    final built = buildParams(params, {'threshold': '85'});
    expect(built.errors, isEmpty);
    expect(built.params, {'threshold': 0.85});
  });

  test('the range is checked in the units it is shown in', () {
    final t = parse(
      template(
        't',
        params: [
          param('threshold', unit: 'ratio', byDefault: 0.8, min: 0.01, max: 1),
        ],
      ),
    );
    final params = editableParams(t);

    // 150 percent is out of range; the message has to talk percent too,
    // which is what displayRange gives it.
    final built = buildParams(params, {'threshold': '150'});
    expect(built.errors['threshold']?.kind, 'range');
    expect(built.errors['threshold']?.max, 100);
    expect(displayRange(params.single), (min: 1.0, max: 100.0));
  });

  test('a comma is a decimal point, and a word is not a number', () {
    final t = parse(
      template('t', params: [param('seconds', unit: 'seconds', byDefault: 60)]),
    );
    final params = editableParams(t);

    expect(buildParams(params, {'seconds': '1,5'}).params, {'seconds': 1.5});
    expect(
      buildParams(params, {'seconds': 'çok'}).errors['seconds']?.kind,
      'number',
    );
    expect(
      buildParams(params, {'seconds': ''}).errors['seconds']?.kind,
      'required',
    );
  });

  test('targets the phone cannot fill are left to the server', () {
    final t = parse(
      template(
        't',
        params: [
          param('threshold'),
          param('host_name', kind: 'host', required: false),
          param('discovery_id', kind: 'text', required: false),
          param('instance', kind: 'instance', required: false),
        ],
      ),
    );

    // Only the threshold is asked for: the other three are a target the web
    // carries from the page it was opened on, and this list has no page.
    expect(editableParams(t).map((p) => p.key), ['threshold']);
  });

  test('ratio templates are not offered without an instance', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/rule-types')) {
        writeJson(req, 200, {
          'types': [
            {'type': 'metric_threshold', 'available': true, 'reason': ''},
            {'type': 'slo_burn', 'available': false, 'reason': 'no SLO yet'},
          ],
        });
        return;
      }
      writeJson(req, 200, {
        'templates': [
          template('plain'),
          template('ratio', referenceMetric: 'node_memory_total'),
        ],
      });
    });
    addTearDown(server.stop);
    final c = TemplatesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    // A ratio template computes its threshold from a reference metric on one
    // instance; with no instance to take a ratio of, offering it would be
    // offering a set-up that cannot finish.
    expect(c.items.map((t) => t.id), ['plain']);
    // And the types this installation cannot use are named with the
    // server's reason, so a set-up that would fail on create is not a
    // surprise at the end of the form.
    expect(c.unavailable.single.type.wire, 'slo_burn');
    expect(c.unavailable.single.reason, 'no SLO yet');
  });

  test(
    'the rule is previewed and created exactly as the server rendered it',
    () async {
      final sent = <(String, Object?)>[];
      final server = await FakeServer.start((req, seen) {
        final body = seen.body.isEmpty ? null : jsonDecode(seen.body);
        sent.add((seen.path, body));
        if (seen.path.endsWith('/render')) {
          writeJson(req, 200, {
            'rule': {
              'name': 'CPU yüksek — web-1',
              'type': 'metric_threshold',
              'condition': {
                'metric': 'system.cpu.utilization',
                'threshold': 0.85,
                // A field this app's contract copy does not declare. It has to
                // survive both trips: a rule with a field missing is a
                // different rule.
                'future_field': 'keep me',
              },
            },
            'reference': null,
          });
        } else if (seen.path.endsWith('/preview')) {
          writeJson(req, 200, {
            'from': '2026-10-10T00:00:00.000000000Z',
            'to': '2026-10-10T06:00:00.000000000Z',
            'step_seconds': 60,
            'operator': 'gt',
            'threshold': 0.85,
            'recovery_threshold': null,
            'unit': 'ratio',
            'series': [
              {
                'key': 'web-1',
                'labels': {'host': 'web-1'},
                'points': [
                  [1760000000000, 0.4],
                  [1760000060000, null],
                ],
                'transitions': <Object>[],
                'incidents': [
                  {
                    'opened_at': '2026-10-10T03:00:00.000000000Z',
                    'resolved_at': null,
                    'peak': 0.9,
                  },
                ],
              },
            ],
            'truncated': false,
            'approximate': false,
          });
        } else {
          writeJson(req, 201, {'id': 'r1'});
        }
      });
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';
      final c = TemplateSetupController(
        client,
        parse(template('cpu_high', params: [param('threshold')])),
        language: 'tr',
      );
      addTearDown(c.dispose);

      await c.check({'threshold': 0.85});
      expect(c.failure, isNull);
      expect(c.preview, isNotNull);
      expect(previewIncidents(c.preview!), 1);
      // A null point is a bucket with no data, not a zero.
      expect(busiestSeries(c.preview!)!.points.last.$2, isNull);

      await c.create();
      expect(c.created, 'CPU yüksek — web-1');

      final preview = sent[1].$2! as Map<String, Object?>;
      final created = sent[2].$2! as Map<String, Object?>;
      final condition = (created['condition']! as Map).cast<String, Object?>();
      expect((preview['rule']! as Map)['condition'], condition);
      expect(condition['future_field'], 'keep me');
    },
  );

  test('a rejected create keeps the server sentence', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/render')) {
        writeJson(req, 200, {
          'rule': {
            'name': 'x',
            'type': 'metric_threshold',
            'condition': <String, Object?>{},
          },
          'reference': null,
        });
      } else if (seen.path.endsWith('/preview')) {
        writeJson(req, 200, {
          'from': '2026-10-10T00:00:00.000000000Z',
          'to': '2026-10-10T06:00:00.000000000Z',
          'step_seconds': 60,
          'operator': 'gt',
          'threshold': null,
          'recovery_threshold': null,
          'unit': '',
          'series': <Object>[],
          'truncated': false,
          'approximate': false,
        });
      } else {
        writeJson(req, 409, {
          'error': {'message': 'a rule with that name exists'},
        });
      }
    });
    addTearDown(server.stop);
    final c = TemplateSetupController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      parse(template('t')),
      language: 'en',
    );
    addTearDown(c.dispose);

    await c.check(const {});
    await c.create();

    expect(c.created, isNull);
    expect(c.failure?.detail, contains('that name exists'));
  });
}
