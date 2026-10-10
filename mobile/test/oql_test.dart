// The query language on this side: what the builder writes, what the schema
// is asked for, and when the validation applies.
//
// The builder's tests are the web's own cases: the two have to produce the
// same text for the same picks, or a query built on a phone is a different
// query from the one built in a browser.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/oql.dart';
import 'package:openlog_mobile/src/oql_builder.dart';

import 'fake_server.dart';

Map<String, Object?> schema({
  String eventType = 'Log',
  List<Map<String, Object?>> attributes = const [
    {
      'name': 'severity',
      'type': 'string',
      'aliases': <String>[],
      'rollup': false,
    },
    {
      'name': 'duration.ms',
      'type': 'number',
      'aliases': <String>[],
      'rollup': false,
    },
  ],
  List<String> attributeKeys = const ['user.id'],
  List<String> resourceKeys = const ['k8s.pod.name'],
  List<String> metricNames = const [],
}) => {
  'event_types': [
    {
      'name': eventType,
      'description': '',
      'maps': ['attributes', 'resource'],
      'max_range_seconds': 86400,
      'attributes': attributes,
    },
  ],
  'functions': [
    {'name': 'count', 'signature': 'count(*)', 'description': ''},
  ],
  'keywords': ['SELECT', 'FROM'],
  'attribute_keys': attributeKeys,
  'resource_keys': resourceKeys,
  'metric_names': metricNames,
};

Map<String, Object?> validation({
  bool valid = true,
  List<Map<String, Object?>> errors = const [],
  List<Map<String, Object?>> warnings = const [],
}) => {
  'valid': valid,
  'event_type': 'Log',
  'kind': valid ? 'timeseries' : null,
  'variables': <String>[],
  'errors': errors,
  'warnings': warnings,
};

Map<String, Object?> diagnostic(
  String message, {
  int line = 1,
  int column = 8,
}) => {
  'message': message,
  'offset': 7,
  'length': 3,
  'line': line,
  'column': column,
};

void main() {
  group('the builder writes what the web writes', () {
    test('count over everything, with a split and a time series', () {
      final s = BuilderState(
        eventType: 'Log',
        groupBy: ['service.name'],
        limit: 5,
      );
      expect(
        buildOql(s),
        'SELECT count(*) FROM Log FACET service.name LIMIT 5 TIMESERIES AUTO',
      );
    });

    test('a map key becomes a lookup, a string value is quoted', () {
      final s = BuilderState(
        eventType: 'Log',
        conditions: [
          BuilderCondition(key: 'attributes.user.id', op: '=', value: 'u-1'),
          BuilderCondition(key: 'severity', op: 'contains', value: "o'brien"),
        ],
        timeseries: false,
      );
      expect(
        buildOql(s),
        "SELECT count(*) FROM Log WHERE attributes['user.id'] = 'u-1' "
        "AND severity CONTAINS 'o''brien'",
      );
    });

    test('numbers and booleans go in unquoted; IS NULL takes no value', () {
      final s = BuilderState(
        eventType: 'Transaction',
        measure: 'percentile',
        attribute: 'duration.ms',
        conditions: [
          BuilderCondition(key: 'http.status_code', op: '>=', value: '500'),
          BuilderCondition(key: 'error', op: '=', value: 'true'),
          BuilderCondition(key: 'user.id', op: 'is_null'),
          // No value, no condition: a half-typed row must not become
          // `x = ''`, which matches nothing and looks deliberate.
          BuilderCondition(key: 'name', op: '=', value: '   '),
        ],
        timeseries: false,
      );
      expect(
        buildOql(s),
        'SELECT percentile(duration.ms, 50, 95, 99) FROM Transaction '
        'WHERE http.status_code >= 500 AND error = true AND user.id IS NULL',
      );
    });

    test('a metric query names its metric first', () {
      final s = BuilderState(
        eventType: 'Metric',
        measure: 'average',
        metricName: 'system.cpu.utilization',
        groupBy: ['host.name'],
      );
      expect(
        buildOql(s),
        "SELECT average(value) FROM Metric "
        "WHERE metricName = 'system.cpu.utilization' "
        'FACET host.name LIMIT 10 TIMESERIES AUTO',
      );
    });

    test('incomplete picks write nothing, and say which pick is missing', () {
      expect(builderIssue(BuilderState(measure: 'average')), 'attribute');
      expect(buildOql(BuilderState(measure: 'average')), '');
      expect(
        builderIssue(BuilderState(eventType: 'Metric', measure: 'count')),
        'metric',
      );
    });
  });

  group('which schema to ask for', () {
    test('the event type after FROM, however it was written', () {
      expect(eventTypeOf('SELECT count(*) FROM Log FACET x'), 'Log');
      expect(canonicalEventType(eventTypeOf('select 1 from metric')), 'Metric');
      expect(eventTypeOf('SELECT count(*)'), '');
      expect(canonicalEventType('Nonsense'), '');
    });
  });

  test('the schema is asked once per event type', () async {
    final asked = <String>[];
    final server = await FakeServer.start((req, seen) {
      asked.add(seen.query);
      writeJson(
        req,
        200,
        schema(
          eventType: seen.query.contains('Metric') ? 'Metric' : 'Log',
          metricNames: seen.query.contains('Metric')
              ? ['system.cpu.utilization']
              : const [],
        ),
      );
    });
    addTearDown(server.stop);
    final c = OqlSchemaController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.load(eventType: 'Log');
    await c.load(eventType: 'Log');
    await c.load(eventType: 'Metric');

    // The plain schema once, then one request per type -- not one per
    // keystroke that happened to change the FROM back and forth.
    expect(asked, ['', 'event_type=Log', 'event_type=Metric']);
    expect(c.metricNames(), ['system.cpu.utilization']);
  });

  test(
    'the keys of a type are its attributes and the maps it really has',
    () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, schema()),
      );
      addTearDown(server.stop);
      final c = OqlSchemaController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.load(eventType: 'Log');

      expect(c.keysOf('Log'), [
        'severity',
        'duration.ms',
        'attributes.user.id',
        'resource.k8s.pod.name',
      ]);
      // A number measure cannot be taken of a text attribute; a map value has
      // no type in the schema, so it stays on offer.
      expect(c.numberKeysOf('Log'), [
        'duration.ms',
        'attributes.user.id',
        'resource.k8s.pod.name',
      ]);
    },
  );

  group('validation', () {
    test('it is asked once the typing stops', () async {
      var calls = 0;
      final server = await FakeServer.start((req, seen) {
        calls++;
        writeJson(req, 200, validation());
      });
      addTearDown(server.stop);
      final c = OqlValidationController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        delay: const Duration(milliseconds: 10),
      );

      c.schedule('SELECT');
      c.schedule('SELECT count');
      c.schedule('SELECT count(*) FROM Log');
      await Future<void>.delayed(const Duration(milliseconds: 60));

      expect(calls, 1);
      expect(c.checked, 'SELECT count(*) FROM Log');
    });

    test('an answer belongs to the query it was asked about', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, {
          ...validation(valid: false),
          'errors': [diagnostic('unexpected token')],
        }),
      );
      addTearDown(server.stop);
      final c = OqlValidationController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.check('SELECT bad');

      expect(c.diagnosticsFor('SELECT bad').single.error, isTrue);
      expect(
        c.diagnosticsFor('SELECT bad').single.diagnostic.message,
        'unexpected token',
      );
      // The box moved on: the positions in that answer point at text that is
      // no longer there, so it says nothing.
      expect(c.diagnosticsFor('SELECT bad things'), isEmpty);
    });

    test('an empty box has nothing to answer for', () async {
      final server = await FakeServer.start(
        (req, seen) => writeJson(req, 200, validation()),
      );
      addTearDown(server.stop);
      final c = OqlValidationController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        delay: const Duration(milliseconds: 5),
      );

      await c.check('SELECT 1');
      c.schedule('   ');
      await Future<void>.delayed(const Duration(milliseconds: 30));

      expect(c.result, isNull);
      expect(c.checked, '');
      expect(server.requests.length, 1);
    });
  });
}
