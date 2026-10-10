// What a log record is made of, and what can be done with one field of it.
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/fields.dart';
import 'package:openlog_mobile/src/log_fields.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/log_detail_screen.dart';

LogQueryRow row({
  String body = 'connection refused',
  int severity = 17,
  Map<String, String>? attributes,
  Map<String, String>? resource,
  Map<String, String> fields = const {},
  String traceId = '',
}) => LogQueryRow(
  id: 'r1',
  timestamp: DateTime.utc(2026, 10, 10, 9),
  observedTimestamp: DateTime.utc(2026, 10, 10, 9),
  severityText: severity >= 17 ? 'ERROR' : '',
  severityNumber: severity,
  body: body,
  serviceName: 'checkout',
  hostId: 'h1',
  hostName: 'web-1',
  traceId: traceId,
  spanId: '',
  fields: fields,
  attributes: attributes,
  resourceAttributes: resource,
);

void main() {
  group('the fields of a record', () {
    test('row fields, then attributes, then resource, then a JSON body', () {
      final fields = recordFields(
        row(
          body: '{"level":"error","retries":3}',
          attributes: {'http.method': 'GET', 'db.system': 'postgresql'},
          resource: {'container.id': 'abc'},
        ),
      );
      expect(
        [for (final f in fields) f.key],
        [
          'timestamp',
          'observed_timestamp',
          'body',
          'severity_text',
          'severity_number',
          'service.name',
          'host.id',
          'host.name',
          // Attributes and resource attributes by name, as the browser
          // lists them -- not in the order a map happened to be built.
          'attributes.db.system',
          'attributes.http.method',
          'resource.container.id',
          'body.level',
          'body.retries',
        ],
      );
      // A JSON value that is not a string is still readable as one.
      expect(fields.last.value, '3');
      expect(fields.last.source, FieldSource.body);
    });

    test('nothing empty, and no severity_number 0', () {
      final fields = recordFields(row(severity: 0));
      final keys = [for (final f in fields) f.key];
      // No severity at all: the number is 0 and the text is empty, and a
      // detail list with "severity_number 0" in it says the opposite of
      // what it means.
      expect(keys, isNot(contains('severity_number')));
      expect(keys, isNot(contains('severity_text')));
      expect(keys, isNot(contains('trace_id')));
      expect(keys, contains('host.name'));
    });

    test('a body that is not a JSON object is only a body', () {
      expect(jsonBody('[1,2]'), isNull);
      expect(jsonBody('not json'), isNull);
      expect(jsonBody('{"a":1}'), {'a': 1});
      expect([
        for (final f in recordFields(row(body: '[1,2]'))) f.key,
      ], isNot(contains('body.0')));
    });
  });

  group('what a column shows', () {
    test('the row itself, then what was asked for, then the maps', () {
      final r = row(
        attributes: {'http.method': 'GET'},
        resource: {'container.id': 'abc'},
        fields: {'resource.k8s.pod.name': 'api-7'},
      );
      expect(cellValue(r, 'service.name'), 'checkout');
      expect(cellValue(r, 'severity_number'), '17');
      expect(cellValue(r, 'resource.k8s.pod.name'), 'api-7');
      // With the prefix and without it: a key picked from the dictionary
      // carries one, a key typed by hand may not.
      expect(cellValue(r, 'attributes.http.method'), 'GET');
      expect(cellValue(r, 'http.method'), 'GET');
      expect(cellValue(r, 'resource.container.id'), 'abc');
      expect(cellValue(r, 'nope'), isNull);
    });
  });

  group('filtering by a value', () {
    test('a number goes on the wire as a number', () {
      expect(valueFilter('severity_number', '17').toJson(), {
        'key': 'severity_number',
        'op': '=',
        'value': 17,
      });
      expect(
        valueFilter('duration', '12', type: FieldType.number).toJson(),
        containsPair('value', 12),
      );
      // And a string that merely looks like one, under a string key, does
      // not: `service.name = 8080` would match nothing.
      expect(
        valueFilter('service.name', '8080').toJson(),
        containsPair('value', '8080'),
      );
    });

    test('excluding is the same condition, negated', () {
      expect(valueFilter('service.name', 'checkout', exclude: true).op, '!=');
    });

    test('a value past the server limit is refused before it is sent', () {
      expect(isFilterableValue('a' * 1024), isTrue);
      expect(isFilterableValue('a' * 1025), isFalse);
      // Bytes, not characters: the limit is on what goes over the wire.
      expect(isFilterableValue('ş' * 513), isFalse);
    });

    test('a number read from a view is written back as a number', () {
      final f = Filter.fromJson({
        'key': 'severity_number',
        'op': '>=',
        'value': 13,
      });
      expect(f!.toJson()['value'], 13);
      expect(
        Filter.fromJson({
          'key': 'service.name',
          'op': '=',
          'value': 'api',
        })!.toJson()['value'],
        'api',
      );
    });
  });

  test('the JSON tab is the record, with the body opened up', () {
    final text = recordJson(row(body: '{"level":"error"}'));
    final m = jsonDecode(text) as Map<String, Object?>;
    expect(m['body'], {'level': 'error'});
    expect(m['service_name'], 'checkout');
    // `fields` is the columns that were asked for, not part of the record.
    expect(m, isNot(contains('fields')));
  });

  group('the detail screen', () {
    Future<List<Filter>> pump(
      WidgetTester tester, {
      required LogQueryRow record,
    }) async {
      final added = <Filter>[];
      final sections = Sections(
        client: OpenlogClient(baseUrl: 'http://127.0.0.1:1'),
      );
      addTearDown(sections.dispose);
      final session = SessionController(store: MemoryTokenStore());
      // Pushed from a list, as it is in the app: the screen pops itself
      // after a filter, and what says so is a snack bar on the page
      // underneath.
      await tester.pumpWidget(
        MaterialApp(
          localizationsDelegates: L.localizationsDelegates,
          supportedLocales: L.supportedLocales,
          locale: const Locale('tr'),
          home: Scaffold(
            body: Builder(
              builder: (context) => TextButton(
                key: const Key('open'),
                onPressed: () => openLogDetail(
                  context,
                  session: session,
                  sections: sections,
                  record: record,
                  onFilter: added.add,
                ),
                child: const Text('open'),
              ),
            ),
          ),
        ),
      );
      await tester.tap(find.byKey(const Key('open')));
      await tester.pumpAndSettle();
      return added;
    }

    testWidgets('shows the body and every field of the record', (tester) async {
      await pump(tester, record: row(attributes: {'http.method': 'GET'}));

      expect(find.text('connection refused'), findsWidgets);
      expect(find.text('attributes.http.method'), findsOneWidget);
      expect(find.text('GET'), findsOneWidget);
      // Twice: the badge at the top and the `severity_text` field below.
      expect(find.text('ERROR'), findsNWidgets(2));
    });

    testWidgets('the search box narrows the fields to what was typed', (
      tester,
    ) async {
      await pump(
        tester,
        record: row(attributes: {'http.method': 'GET', 'db.system': 'pg'}),
      );

      await tester.enterText(find.byKey(const Key('log-field-search')), 'http');
      await tester.pumpAndSettle();

      expect(find.text('attributes.http.method'), findsOneWidget);
      expect(find.text('attributes.db.system'), findsNothing);
      // The value matches too, which is how somebody finds the field a
      // value they already know is in.
      await tester.enterText(find.byKey(const Key('log-field-search')), 'pg');
      await tester.pumpAndSettle();
      expect(find.text('attributes.db.system'), findsOneWidget);
    });

    testWidgets('a field filters the list and says so', (tester) async {
      final added = await pump(
        tester,
        record: row(attributes: {'http.method': 'GET'}),
      );

      final menu = find.byKey(const Key('log-field-attributes.http.method'));
      await tester.ensureVisible(menu);
      await tester.pumpAndSettle();
      await tester.tap(menu);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Bu değere göre filtrele'));
      await tester.pumpAndSettle();

      expect(added.single.key, 'attributes.http.method');
      expect(added.single.op, '=');
      expect(added.single.values, ['GET']);
      // Back to the list, which is where the answer to the condition is.
      expect(find.byKey(const Key('log-filter-added')), findsOneWidget);
      expect(find.byType(LogDetailScreen), findsNothing);
    });

    testWidgets('a value too long to filter by says so and changes nothing', (
      tester,
    ) async {
      final added = await pump(
        tester,
        record: row(attributes: {'stack': 'x' * 2000}),
      );

      final menu = find.byKey(const Key('log-field-attributes.stack'));
      await tester.ensureVisible(menu);
      await tester.pumpAndSettle();
      await tester.tap(menu);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Bu değeri hariç tut'));
      await tester.pumpAndSettle();

      expect(added, isEmpty);
      expect(find.byKey(const Key('log-value-too-long')), findsOneWidget);
      expect(find.byType(LogDetailScreen), findsOneWidget);
    });
  });
}
