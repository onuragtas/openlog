// The console screen: the guided builder, the problems under the box and
// the examples.
//
// Scripted controllers rather than a server, like the other widget tests:
// what matters here is that a pick becomes a query, that a problem is shown
// against the text it belongs to, and that an example runs.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/oql.dart';
import 'package:openlog_mobile/src/query.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/query_screen.dart';

final _client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');

OqlSchema oqlSchema({
  String eventType = 'Log',
  List<String> metricNames = const [],
}) => OqlSchema(
  eventTypes: [
    OqlSchemaEventTypesItem(
      name: eventType,
      description: '',
      maps: const [OqlSchemaEventTypesItemMapsItem.attributes],
      maxRangeSeconds: 86400,
      attributes: const [
        OqlSchemaEventTypesItemAttributesItem(
          name: 'service.name',
          type: OqlSchemaEventTypesItemAttributesItemType.string,
          aliases: [],
          rollup: false,
        ),
        OqlSchemaEventTypesItemAttributesItem(
          name: 'duration.ms',
          type: OqlSchemaEventTypesItemAttributesItemType.number,
          aliases: [],
          rollup: false,
        ),
      ],
    ),
  ],
  functions: const [],
  keywords: const [],
  attributeKeys: const [],
  resourceKeys: const [],
  metricNames: metricNames,
);

class ScriptedSchema extends OqlSchemaController {
  ScriptedSchema() : super(_client) {
    base = oqlSchema();
    detail['Log'] = oqlSchema();
  }

  @override
  Future<void> load({String eventType = ''}) async {}
}

class ScriptedValidation extends OqlValidationController {
  ScriptedValidation({this.answer}) : super(_client);

  OqlValidation? answer;

  /// Answers at once: the debounce is tested where it lives, and a timer
  /// inside a widget test would only be a slower way to the same screen.
  @override
  void schedule(String query) {
    if (query.trim().isEmpty) {
      checked = '';
      result = null;
      notifyListeners();
      return;
    }
    checked = query.trim();
    // Only the bad query is bad: the fake answers about what it was asked,
    // like the server, so the screen's own gating is what is being tested.
    result = checked.contains('bad') ? answer : null;
    notifyListeners();
  }
}

class ScriptedQuery extends QueryController {
  ScriptedQuery() : super(_client);

  final calls = <String>[];

  @override
  Future<void> run(String query) async {
    calls.add(query);
    ran = query;
    notifyListeners();
  }
}

OqlValidation rejected(String message) => OqlValidation(
  valid: false,
  eventType: null,
  kind: null,
  variables: const [],
  errors: [
    OqlDiagnostic(message: message, offset: 7, length: 3, line: 1, column: 8),
  ],
  warnings: const [],
);

Future<(ScriptedQuery, Sections)> pump(
  WidgetTester tester, {
  ScriptedValidation? validation,
}) async {
  final query = ScriptedQuery();
  final sections = Sections(
    client: _client,
    query: query,
    oqlSchema: ScriptedSchema(),
    oqlValidation: validation ?? ScriptedValidation(),
  );
  addTearDown(sections.dispose);
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('tr'),
      home: Scaffold(
        body: QueryBody(
          session: SessionController(store: MemoryTokenStore()),
          sections: sections,
          query: query,
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return (query, sections);
}

void main() {
  testWidgets('the builder writes the query and runs it', (tester) async {
    final (query, _) = await pump(tester);

    // The console opens in the builder, as the web's does.
    expect(find.byKey(const Key('query-wizard')), findsOneWidget);

    await tester.tap(find.byKey(const Key('wizard-add-group')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('pick-service.name')));
    await tester.pumpAndSettle();

    expect(
      tester.widget<Text>(find.byKey(const Key('wizard-preview'))).data,
      'SELECT count(*) FROM Log FACET service.name LIMIT 10 TIMESERIES AUTO',
    );

    await tester.tap(find.byKey(const Key('wizard-run')));
    await tester.pumpAndSettle();

    expect(query.calls, [
      'SELECT count(*) FROM Log FACET service.name LIMIT 10 TIMESERIES AUTO',
    ]);
    // Running from the builder puts the query in the box, so it can be
    // changed by hand afterwards -- which is the point of the builder.
    expect(
      tester
          .widget<TextField>(find.byKey(const Key('query-text')))
          .controller!
          .text,
      startsWith('SELECT count(*) FROM Log'),
    );
  });

  testWidgets('a measure needs a field, and the preview says so', (
    tester,
  ) async {
    await pump(tester);

    await tester.tap(find.byKey(const Key('wizard-measure')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Ortalaması').last);
    await tester.pumpAndSettle();

    expect(
      tester.widget<Text>(find.byKey(const Key('wizard-preview'))).data,
      'Ölçülecek alanı seçin.',
    );
    expect(
      tester
          .widget<FilledButton>(find.byKey(const Key('wizard-run')))
          .onPressed,
      isNull,
    );

    await tester.tap(find.byKey(const Key('wizard-attribute')));
    await tester.pumpAndSettle();
    // Only the number fields are offered for an average; the map keys that
    // have no type stay on offer, but a text attribute does not.
    expect(find.byKey(const Key('pick-service.name')), findsNothing);
    await tester.tap(find.byKey(const Key('pick-duration.ms')));
    await tester.pumpAndSettle();

    expect(
      tester.widget<Text>(find.byKey(const Key('wizard-preview'))).data,
      'SELECT average(duration.ms) FROM Log TIMESERIES AUTO',
    );
  });

  testWidgets('what the server says is wrong is shown under the box', (
    tester,
  ) async {
    await pump(
      tester,
      validation: ScriptedValidation(answer: rejected('unexpected token')),
    );
    await tester.tap(find.byKey(const Key('query-wizard-toggle')));
    await tester.pumpAndSettle();

    await tester.enterText(
      find.byKey(const Key('query-text')),
      'SELECT bad FROM Log',
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('query-problems')), findsOneWidget);
    expect(
      find.text('Hata: Satır 1, sütun 8: unexpected token'),
      findsOneWidget,
    );

    // Typed on: the problem belonged to the old text, and the new text has
    // not been answered for yet.
    await tester.enterText(find.byKey(const Key('query-text')), 'SELECT');
    await tester.pumpAndSettle();
    expect(find.text('Hata: Satır 1, sütun 8: unexpected token'), findsNothing);
  });

  testWidgets('an example runs as it is written', (tester) async {
    final (query, _) = await pump(tester);

    await tester.tap(find.byKey(const Key('query-examples-toggle')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('example-logsBySeverity')));
    await tester.pumpAndSettle();

    expect(query.calls, ['SELECT count(*) FROM Log FACET severity TIMESERIES']);
  });
}
