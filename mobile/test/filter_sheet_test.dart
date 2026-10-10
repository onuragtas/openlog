// The filter builder's two steps: the keys the data has, then the values
// that key holds.
//
// The search box is the thing being tested here. It used to narrow the
// list only when the keyboard's search key was pressed, so typing into it
// looked like a box that did nothing.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/fields.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/filter_sheet.dart';

final _client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');

FieldKey fieldKey(String name) => FieldKey(
  key: name,
  name: name,
  type: FieldType.string,
  source: FieldSource.attribute,
  count: 120,
  cardinality: 7,
);

FieldValue fieldValue(String v) => FieldValue(value: v, count: 9);

class ScriptedFields extends FieldsController {
  ScriptedFields() : super(_client, signal: 'logs') {
    keys = [
      fieldKey('service.name'),
      fieldKey('host.name'),
      fieldKey('http.status_code'),
      fieldKey('severity_text'),
    ];
  }

  final asked = <String>[];

  @override
  Future<void> loadKeys({String q = ''}) async {
    asked.add('keys:$q');
    notifyListeners();
  }

  @override
  Future<void> loadValues(
    String forKey, {
    String q = '',
    String metric = '',
  }) async {
    asked.add('values:$forKey:$q');
    key = forKey;
    values = [fieldValue('checkout'), fieldValue('cart'), fieldValue('search')];
    notifyListeners();
  }
}

Future<Filter?> open(WidgetTester tester, ScriptedFields fields) async {
  Filter? picked;
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('tr'),
      home: Scaffold(
        body: Builder(
          builder: (context) => Center(
            child: TextButton(
              onPressed: () async {
                picked = await pickFilter(
                  context,
                  session: SessionController(store: MemoryTokenStore()),
                  fields: fields,
                );
              },
              child: const Text('aç'),
            ),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('aç'));
  await tester.pumpAndSettle();
  return picked;
}

void main() {
  testWidgets('typing narrows the keys at once, without a round trip', (
    tester,
  ) async {
    final fields = ScriptedFields();
    addTearDown(fields.dispose);
    await open(tester, fields);

    expect(find.byKey(const Key('filter-key-service.name')), findsOneWidget);
    expect(find.byKey(const Key('filter-key-host.name')), findsOneWidget);

    await tester.enterText(find.byKey(const Key('filter-search')), 'host');
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('filter-key-host.name')), findsOneWidget);
    expect(find.byKey(const Key('filter-key-service.name')), findsNothing);
    // Narrowed from what the server already sent: no request was made.
    expect(fields.asked, ['keys:']);
  });

  testWidgets('pressing search asks the server for the rest', (tester) async {
    final fields = ScriptedFields();
    addTearDown(fields.dispose);
    await open(tester, fields);

    await tester.enterText(find.byKey(const Key('filter-search')), 'pod');
    await tester.testTextInput.receiveAction(TextInputAction.search);
    await tester.pumpAndSettle();

    // The loaded page is not all there is, so the box can still ask.
    expect(fields.asked, ['keys:', 'keys:pod']);
  });

  testWidgets('the same box narrows the values of a key', (tester) async {
    final fields = ScriptedFields();
    addTearDown(fields.dispose);
    await open(tester, fields);

    await tester.tap(find.byKey(const Key('filter-key-service.name')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('filter-value-checkout')), findsOneWidget);

    await tester.enterText(find.byKey(const Key('filter-search')), 'car');
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('filter-value-cart')), findsOneWidget);
    expect(find.byKey(const Key('filter-value-checkout')), findsNothing);
  });

  testWidgets('a ticked value stays on the list whatever is typed', (
    tester,
  ) async {
    final fields = ScriptedFields();
    addTearDown(fields.dispose);
    await open(tester, fields);

    await tester.tap(find.byKey(const Key('filter-key-service.name')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('filter-value-checkout')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const Key('filter-search')), 'zzz');
    await tester.pumpAndSettle();

    // Otherwise it could not be unticked, and it would still be in the
    // filter that gets applied.
    expect(find.byKey(const Key('filter-value-checkout')), findsOneWidget);
  });
}
